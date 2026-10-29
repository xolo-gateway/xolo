package proxy

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// sharedUsageStore answers the per-model sums a shared constraint reads, for
// the whole plan or for one user, and reports a fixed number of active users.
type sharedUsageStore struct {
	port.UsageStore

	// value is the consumption of each model, per user.
	value  map[model.UserID]map[model.LLMModelID]int64
	active int64
	// plainSums counts the unweighted plan sums, which a shared constraint
	// must not read.
	plainSums int
}

func (s *sharedUsageStore) SumPlanUsageByModelSince(_ context.Context, _ model.PlanUsageScope, userID model.UserID, _ time.Time) (map[model.LLMModelID]port.PlanUsageTotals, error) {
	totals := map[model.LLMModelID]port.PlanUsageTotals{}
	for user, byModel := range s.value {
		if userID != "" && user != userID {
			continue
		}
		for id, v := range byModel {
			t := totals[id]
			t.Value += v
			totals[id] = t
		}
	}
	return totals, nil
}

func (s *sharedUsageStore) SumPlanUsageSince(context.Context, model.PlanUsageScope, time.Time) (int64, int64, error) {
	s.plainSums++
	return 0, 0, nil
}

func (s *sharedUsageStore) SumUserPlanUsageSince(context.Context, model.UserID, model.PlanUsageScope, time.Time) (int64, int64, error) {
	s.plainSums++
	return 0, 0, nil
}

func (s *sharedUsageStore) CountActivePlanUsersSince(context.Context, model.PlanUsageScope, time.Time, model.UserID) (int64, error) {
	return s.active, nil
}

func (s *sharedUsageStore) HasPlanUsageSince(context.Context, model.UserID, model.PlanUsageScope, time.Time) (bool, error) {
	return true, nil
}

// sharedConstraint is a 60$ monthly window, weighted by the budgets given.
func sharedConstraint(overrides map[string]model.PlanBudget) model.PlanConstraint {
	return model.PlanConstraint{
		Kind:         model.ConstraintRollingWindow,
		Label:        "month",
		Duration:     model.PlanDuration(720 * time.Hour),
		ValueBudget:  int64Ptr(60_000_000),
		Scope:        model.PlanScopeShared,
		ModelBudgets: overrides,
	}
}

type sharedFixture struct {
	*enforcerFixture
	shared *sharedUsageStore
}

func newSharedFixture(t *testing.T, members int64, constraints ...model.PlanConstraint) *sharedFixture {
	t.Helper()
	f := &sharedFixture{
		enforcerFixture: newEnforcerFixture(t, constraints...),
		shared:          &sharedUsageStore{value: map[model.UserID]map[model.LLMModelID]int64{}, active: members},
	}
	f.enforcer = NewXoloSubscriptionEnforcer(f.providers, f.shared, service.NewFairShareService(f.shared), f.state, &enforcerOrgStore{members: members})
	return f
}

func (f *sharedFixture) consume(user model.UserID, m model.LLMModel, value int64) {
	if f.shared.value[user] == nil {
		f.shared.value[user] = map[model.LLMModelID]int64{}
	}
	f.shared.value[user][m.ID()] += value
}

// denialFor runs the pre-request check for a user and returns the denial
// message, or "".
func (f *sharedFixture) denialFor(t *testing.T, user model.UserID, m model.LLMModel) string {
	t.Helper()
	req := f.request(m)
	req.UserID = string(user)
	res, err := f.enforcer.PreRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	if res == nil || res.Response == nil {
		return ""
	}
	return res.Response.Body.(map[string]any)["error"].(map[string]any)["message"].(string)
}

func TestSubscriptionEnforcer_SharedConstraintWeighsEachModelByItsOwnBudget(t *testing.T) {
	f := newSharedFixture(t, 0, sharedConstraint(map[string]model.PlanBudget{
		"glm-5.3-flash": {ValueBudget: int64Ptr(180_000_000)},
	}))
	// 150$ of a model whose own budget is three times the window's weighs 50$.
	f.consume("", f.glm, 150_000_000)

	if msg := f.denialFor(t, "", f.kimi); msg != "" {
		t.Fatalf("kimi denied (%q), want granted: glm's 150$ weigh 50$ of the 60$ window", msg)
	}

	// kimi takes the constraint's own rate: 10$ more fill the window, for
	// every model alike.
	f.consume("", f.kimiFast, 10_000_000)
	for _, m := range []model.LLMModel{f.kimi, f.glm} {
		if msg := f.denialFor(t, "", m); msg == "" {
			t.Errorf("%s granted, want denied: the shared window is full", m.ProxyName())
		} else if strings.Contains(msg, "·") {
			t.Errorf("message = %q, want the constraint named alone: the window is not a model's", msg)
		}
	}
	if f.shared.plainSums != 0 {
		t.Errorf("%d unweighted sums read, want none on a shared constraint", f.shared.plainSums)
	}
}

func TestSubscriptionEnforcer_SharedConstraintUnlimitedModelWeighsNothing(t *testing.T) {
	f := newSharedFixture(t, 0, sharedConstraint(map[string]model.PlanBudget{
		"kimi-k3": {},
	}))
	f.consume("", f.kimi, 500_000_000)

	if msg := f.denialFor(t, "", f.glm); msg != "" {
		t.Errorf("glm denied (%q), want granted: an unlimited model does not fill the window", msg)
	}
}

func TestSubscriptionEnforcer_SharedConstraintWeighsTheUserShare(t *testing.T) {
	// Two active members of two: each holds 30$ of the 60$ window.
	f := newSharedFixture(t, 2, sharedConstraint(map[string]model.PlanBudget{
		"glm-5.3-flash": {ValueBudget: int64Ptr(180_000_000)},
	}))

	// 60$ of glm weigh 20$: under the 30$ share, where the plain sum is not.
	f.consume("alice", f.glm, 60_000_000)
	if msg := f.denialFor(t, "alice", f.kimi); msg != "" {
		t.Fatalf("alice denied (%q), want granted: her glm usage weighs 20$ of her 30$ share", msg)
	}

	f.consume("alice", f.kimi, 10_000_000)
	msg := f.denialFor(t, "alice", f.glm)
	if !strings.Contains(msg, "fair-share") {
		t.Errorf("message = %q, want a fair-share denial once her weighed usage reaches 30$", msg)
	}
	if msg := f.denialFor(t, "bob", f.glm); msg != "" {
		t.Errorf("bob denied (%q), want granted: alice's usage is not his", msg)
	}
}

func TestSubscriptionEnforcer_SharedConstraintWithoutModelsWeighsAtTheHighestRate(t *testing.T) {
	f := newSharedFixture(t, 0, sharedConstraint(map[string]model.PlanBudget{
		"glm-5.3-flash": {ValueBudget: int64Ptr(180_000_000)},
		"kimi-k3":       {ValueBudget: int64Ptr(30_000_000)},
	}))
	f.providers.listErr = errors.New("store down")
	// 30$ of glm weigh 10$ when the models are known; without them they weigh
	// at kimi's rate, 60$/30$: 60$, the whole window.
	f.consume("", f.glm, 30_000_000)

	if msg := f.denialFor(t, "", f.glm); msg == "" {
		t.Error("glm granted, want denied: without the models every usage counts at the highest rate, closing early rather than late")
	}
}

func TestSubscriptionEnforcer_Upstream429CoolsDownASharedWindowForEveryModel(t *testing.T) {
	f := newSharedFixture(t, 0, sharedConstraint(nil))

	upstream := &llm.HTTPError{StatusCode: http.StatusTooManyRequests}
	if _, err := f.enforcer.OnError(context.Background(), f.request(f.kimi), upstream); err != nil {
		t.Fatalf("OnError: %v", err)
	}

	if msg := f.denialFor(t, "", f.glm); !strings.Contains(msg, "cooldown") {
		t.Errorf("glm message = %q, want a cooldown: the window is shared by every model", msg)
	}
}

func TestSubscriptionEnforcer_Upstream429LeavesASharedWindowOpenInAPerModelPlan(t *testing.T) {
	// The 429 is attributed to the requested model, as for the provider-wide
	// constraints of such a plan: it cannot be told apart from that model's
	// own exhaustion.
	f := newSharedFixture(t, 0, perModelConstraint("5h", 3_000_000, nil), sharedConstraint(nil))

	upstream := &llm.HTTPError{StatusCode: http.StatusTooManyRequests}
	if _, err := f.enforcer.OnError(context.Background(), f.request(f.kimi), upstream); err != nil {
		t.Fatalf("OnError: %v", err)
	}

	if msg := f.denialFor(t, "", f.glm); msg != "" {
		t.Errorf("glm denied (%q), want granted: only kimi's per-model window is in cooldown", msg)
	}
	if msg := f.denialFor(t, "", f.kimi); !strings.Contains(msg, "cooldown") {
		t.Errorf("kimi message = %q, want a cooldown", msg)
	}
}

func TestSubscriptionEnforcer_MixedPlanListsTheModelsOnce(t *testing.T) {
	f := newSharedFixture(t, 0, perModelConstraint("5h", 3_000_000, nil), sharedConstraint(nil))
	f.providers.listErr = errors.New("store down")

	f.denialFor(t, "", f.kimi)
	if f.providers.listCalls != 1 {
		t.Errorf("ListLLMModels called %d times, want 1: per-model and shared constraints share the read", f.providers.listCalls)
	}
}
