package proxy

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// poolUsageStore answers the plan sums from value totals per user and per
// key, and remembers the key of each user's last request.
type poolUsageStore struct {
	port.UsageStore

	value map[model.UserID]map[model.ProviderKeyID]int64
	last  map[model.UserID]model.ProviderKeyID
	// byKeyReads counts the reads of every key's usage at once.
	byKeyReads int
}

func (s *poolUsageStore) sum(scope model.PlanUsageScope, user model.UserID) int64 {
	var total int64
	for u, byKey := range s.value {
		if user != "" && u != user {
			continue
		}
		for key, v := range byKey {
			if !scope.ByKey || key == scope.KeyID {
				total += v
			}
		}
	}
	return total
}

func (s *poolUsageStore) SumPlanUsageSince(_ context.Context, scope model.PlanUsageScope, _ time.Time) (int64, int64, error) {
	return 0, s.sum(scope, ""), nil
}

func (s *poolUsageStore) SumUserPlanUsageSince(_ context.Context, user model.UserID, scope model.PlanUsageScope, _ time.Time) (int64, int64, error) {
	return 0, s.sum(scope, user), nil
}

func (s *poolUsageStore) CountActivePlanUsersSince(_ context.Context, _ model.PlanUsageScope, _ time.Time, exclude model.UserID) (int64, error) {
	var n int64
	for u := range s.value {
		if u != exclude {
			n++
		}
	}
	return n, nil
}

func (s *poolUsageStore) HasPlanUsageSince(_ context.Context, user model.UserID, _ model.PlanUsageScope, _ time.Time) (bool, error) {
	_, ok := s.value[user]
	return ok, nil
}

func (s *poolUsageStore) SumPlanUsageByKeySince(_ context.Context, _ model.PlanUsageScope, _ time.Time) (map[model.ProviderKeyID]map[model.LLMModelID]port.PlanUsageTotals, error) {
	s.byKeyReads++
	result := map[model.ProviderKeyID]map[model.LLMModelID]port.PlanUsageTotals{}
	for _, byKey := range s.value {
		for key, v := range byKey {
			if result[key] == nil {
				result[key] = map[model.LLMModelID]port.PlanUsageTotals{}
			}
			t := result[key]["kimi"]
			t.Value += v
			result[key]["kimi"] = t
		}
	}
	return result, nil
}

func (s *poolUsageStore) LastPlanUsageKey(_ context.Context, _ model.PlanUsageScope, user model.UserID) (model.ProviderKeyID, bool, error) {
	key, ok := s.last[user]
	return key, ok, nil
}

type poolFixture struct {
	enforcer *XoloSubscriptionEnforcer
	state    *SubscriptionStateDetailed
	usage    *poolUsageStore
	provider *model.BaseProvider
	kimi     model.LLMModel
}

// newPoolFixture serves a provider holding its own key and two more, "k2"
// and "k3", each with a 10$ plan.
func newPoolFixture(t *testing.T, members int64) *poolFixture {
	t.Helper()
	p := model.NewProvider("org-1", "OpenCode Go", "openai", "https://opencode.ai/zen/go/v1", "key", "USD")
	p.SetBillingMode(model.BillingModeSubscription)
	p.SetSubscriptionPlan(&model.SubscriptionPlan{Label: "Go", Constraints: []model.PlanConstraint{{
		Kind:        model.ConstraintRollingWindow,
		Label:       "5h",
		Duration:    model.PlanDuration(5 * time.Hour),
		ValueBudget: int64Ptr(10_000_000),
	}}})
	p.SetExtraAPIKeys([]model.ProviderKey{{ID: "k2", Label: "member 2", APIKey: "key2"}, {ID: "k3", Label: "member 3", APIKey: "key3"}})

	f := &poolFixture{
		state:    NewSubscriptionState(),
		usage:    &poolUsageStore{value: map[model.UserID]map[model.ProviderKeyID]int64{}, last: map[model.UserID]model.ProviderKeyID{}},
		provider: p,
		kimi:     model.NewLLMModel(p.ID(), "org-1", "kimi", "kimi-k3", "", 0, 0),
	}
	providers := &enforcerProviderStore{provider: p, models: []model.LLMModel{f.kimi}}
	f.enforcer = NewXoloSubscriptionEnforcer(providers, f.usage, service.NewFairShareService(f.usage), f.state, &enforcerOrgStore{members: members})
	return f
}

func (f *poolFixture) consume(user model.UserID, key model.ProviderKeyID, value int64) {
	if f.usage.value[user] == nil {
		f.usage.value[user] = map[model.ProviderKeyID]int64{}
	}
	f.usage.value[user][key] += value
	f.usage.last[user] = key
}

// check runs the pre-request check for a user and returns the request, with
// the key chosen in its metadata, and the denial message, or "".
func (f *poolFixture) check(t *testing.T, user model.UserID) (*genaiProxy.ProxyRequest, string) {
	t.Helper()
	req := &genaiProxy.ProxyRequest{
		Model:    "org/kimi",
		UserID:   string(user),
		Metadata: map[string]any{MetaOrgID: "org-1", MetaModelID: string(f.kimi.ID())},
	}
	res, err := f.enforcer.PreRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	if res == nil || res.Response == nil {
		return req, ""
	}
	return req, res.Response.Body.(map[string]any)["error"].(map[string]any)["message"].(string)
}

func chosenKey(t *testing.T, req *genaiProxy.ProxyRequest) model.ProviderKeyID {
	t.Helper()
	key, ok := req.Metadata[MetaProviderKey].(model.ProviderKeyID)
	if !ok {
		t.Fatal("no provider key chosen")
	}
	return key
}

func TestSubscriptionEnforcer_PoolSpreadsNewcomersOverTheKeys(t *testing.T) {
	f := newPoolFixture(t, 0)
	keys := model.ProviderKeyIDs(f.provider)

	chosen := map[model.ProviderKeyID]bool{}
	for _, user := range []model.UserID{"u1", "u2", "u3", "u4", "u5", "u6", "u7", "u8"} {
		req, msg := f.check(t, user)
		if msg != "" {
			t.Fatalf("%s denied (%q), want granted", user, msg)
		}
		key := chosenKey(t, req)
		if want := rankKeys(user, keys)[0]; key != want {
			t.Errorf("%s on %q, want %q: on an idle plan each user takes their own first key", user, key, want)
		}
		chosen[key] = true
	}
	if len(chosen) < 2 {
		t.Errorf("every newcomer landed on the same key, want them spread")
	}
}

func TestSubscriptionEnforcer_PoolKeepsAUserOnTheirKey(t *testing.T) {
	f := newPoolFixture(t, 0)
	f.consume("alice", "k2", 9_000_000)

	req, msg := f.check(t, "alice")
	if msg != "" {
		t.Fatalf("alice denied (%q), want granted", msg)
	}
	if key := chosenKey(t, req); key != "k2" {
		t.Errorf("alice moved to %q, want kept on k2: it has room left and holds her cache", key)
	}
}

func TestSubscriptionEnforcer_PoolMovesAUserOffAnExhaustedKey(t *testing.T) {
	f := newPoolFixture(t, 0)
	f.consume("alice", "k2", 10_000_000)
	f.consume("bob", model.PrimaryProviderKey, 5_000_000)
	f.consume("carol", "k3", 2_000_000)

	req, msg := f.check(t, "alice")
	if msg != "" {
		t.Fatalf("alice denied (%q), want granted on another key", msg)
	}
	if key := chosenKey(t, req); key != "k3" {
		t.Errorf("alice moved to %q, want k3, the key with the most room left", key)
	}
	fallbacks, _ := req.Metadata[metaProviderKeyFallbacks].([]model.ProviderKeyID)
	if !slices.Equal(fallbacks, []model.ProviderKeyID{model.PrimaryProviderKey}) {
		t.Errorf("fallbacks = %q, want the provider's own key alone: k2 is exhausted", fallbacks)
	}
}

func TestSubscriptionEnforcer_PoolDeniesWithoutNamingKeysOnceEveryKeyIsFull(t *testing.T) {
	// Every key full is the pool full, refused on the pool's budget.
	f := newPoolFixture(t, 0)
	f.consume("alice", "k2", 10_000_000)
	f.consume("bob", model.PrimaryProviderKey, 10_000_000)
	f.consume("carol", "k3", 10_000_000)

	_, msg := f.check(t, "alice")
	if msg == "" {
		t.Fatal("alice granted, want denied: every key is used up")
	}
	// The pool's budget is what the users' dashboards show; a key's is not.
	for _, leak := range []string{"k2", "k3", "member", "key", "10.00"} {
		if strings.Contains(msg, leak) {
			t.Errorf("message = %q, want nothing about keys nor their budgets (%q)", msg, leak)
		}
	}
}

func TestSubscriptionEnforcer_PoolDecidesSharesOnTheWholePool(t *testing.T) {
	// Two members of two, both active, on three 10$ keys: each holds half of
	// the 30$ pool, far more than half of one key.
	f := newPoolFixture(t, 2)
	f.consume("bob", "k3", 1_000_000)
	f.consume("alice", model.PrimaryProviderKey, 6_000_000)
	f.consume("alice", "k2", 6_000_000)

	if _, msg := f.check(t, "alice"); msg != "" {
		t.Fatalf("alice denied (%q), want granted: her 12$ are under her share of the 30$ pool", msg)
	}

	f.consume("alice", "k2", 4_000_000)
	_, msg := f.check(t, "alice")
	if !strings.Contains(msg, "fair-share") {
		t.Errorf("message = %q, want a fair-share denial once she takes more than her share of the pool", msg)
	}
	if _, msg := f.check(t, "bob"); msg != "" {
		t.Errorf("bob denied (%q), want granted: alice's usage is not his", msg)
	}
}

func TestSubscriptionEnforcer_PoolCoolsDownTheKeyThe429CameFrom(t *testing.T) {
	f := newPoolFixture(t, 0)
	f.consume("alice", "k2", 1_000_000)

	req, _ := f.check(t, "alice")
	req.Metadata[MetaProviderKeyUsed] = model.ProviderKeyID("k2")
	if _, err := f.enforcer.OnError(context.Background(), req, &llm.HTTPError{StatusCode: http.StatusTooManyRequests}); err != nil {
		t.Fatalf("OnError: %v", err)
	}

	if !f.state.IsKeyExhausted("org-1", f.provider.ID(), "k2", "5h", "") {
		t.Error("k2 not in cooldown")
	}
	if f.state.IsKeyExhausted("org-1", f.provider.ID(), model.PrimaryProviderKey, "5h", "") {
		t.Error("the provider's own key in cooldown, want only the key the 429 came from")
	}
	req, msg := f.check(t, "alice")
	if msg != "" {
		t.Fatalf("alice denied (%q), want moved to another key", msg)
	}
	if key := chosenKey(t, req); key == "k2" {
		t.Error("alice kept on k2, want moved off the key in cooldown")
	}
}

func TestSubscriptionEnforcer_SingleKeyProviderChoosesNoKey(t *testing.T) {
	f := newEnforcerFixture(t, perModelConstraint("5h", 3_000_000, nil))

	req := f.request(f.kimi)
	if _, err := f.enforcer.PreRequest(context.Background(), req); err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	if _, ok := req.Metadata[MetaProviderKey]; ok {
		t.Error("a key was chosen for a provider holding its own key alone")
	}
}

func TestSubscriptionEnforcer_PoolDeniesWithoutNamingKeysWhenNoKeyHasRoom(t *testing.T) {
	// 26$ of the 30$ pool, but no key with room left for a request: refused on
	// the keys, whose budgets the message does not give.
	f := newPoolFixture(t, 0)
	f.consume("alice", "k2", 10_000_000)
	f.consume("bob", model.PrimaryProviderKey, 10_000_000)
	f.consume("carol", "k3", 6_000_000)
	f.state.MarkExhausted("org-1", f.provider.ID(), "k3", "5h", "", time.Now().Add(time.Hour))

	_, msg := f.check(t, "alice")
	if msg == "" {
		t.Fatal("alice granted, want denied: no key has room left")
	}
	for _, leak := range []string{"k2", "k3", "member", "key", "$"} {
		if strings.Contains(msg, leak) {
			t.Errorf("message = %q, want nothing about keys nor their budgets (%q)", msg, leak)
		}
	}
}

// rankKeys orders the keys of a pool for one user, the order keys with as
// much room left are picked in.
func rankKeys(userID model.UserID, keys []model.ProviderKeyID) []model.ProviderKeyID {
	ranked := slices.Clone(keys)
	slices.SortStableFunc(ranked, func(a, b model.ProviderKeyID) int {
		return cmp.Compare(model.KeyAffinity(userID, a), model.KeyAffinity(userID, b))
	})
	return ranked
}

func TestSubscriptionEnforcer_PoolReadsEveryKeyAtOnce(t *testing.T) {
	f := newPoolFixture(t, 0)
	f.consume("alice", "k2", 10_000_000)

	if _, msg := f.check(t, "alice"); msg != "" {
		t.Fatalf("alice denied (%q), want granted on another key", msg)
	}
	if f.usage.byKeyReads != 1 {
		t.Errorf("%d reads of the keys' usage, want 1 for the plan's one window whatever the number of keys", f.usage.byKeyReads)
	}
}

func TestSubscriptionEnforcer_PoolLeavesFullKeysOutOfTheFallbacks(t *testing.T) {
	f := newPoolFixture(t, 0)
	f.consume("alice", "k2", 1_000_000)
	f.consume("bob", model.PrimaryProviderKey, 10_000_000)
	f.consume("carol", "k3", 3_000_000)

	req, msg := f.check(t, "alice")
	if msg != "" {
		t.Fatalf("alice denied (%q), want granted", msg)
	}
	if key := chosenKey(t, req); key != "k2" {
		t.Errorf("alice on %q, want kept on k2", key)
	}
	fallbacks, _ := req.Metadata[metaProviderKeyFallbacks].([]model.ProviderKeyID)
	if !slices.Equal(fallbacks, []model.ProviderKeyID{"k3"}) {
		t.Errorf("fallbacks = %q, want k3 alone: the provider's own key is used up", fallbacks)
	}
}
