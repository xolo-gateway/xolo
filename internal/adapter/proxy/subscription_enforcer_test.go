package proxy

import (
	"context"
	stderrors "errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// enforcerProviderStore serves one provider and its models.
type enforcerProviderStore struct {
	port.ProviderStore

	provider  model.Provider
	models    []model.LLMModel
	listCalls int
	listErr   error
}

func (s *enforcerProviderStore) GetProviderByID(_ context.Context, id model.ProviderID) (model.Provider, error) {
	if s.provider.ID() != id {
		return nil, errors.WithStack(port.ErrNotFound)
	}
	return s.provider, nil
}

func (s *enforcerProviderStore) GetLLMModelByID(_ context.Context, id model.LLMModelID) (model.LLMModel, error) {
	for _, m := range s.models {
		if m.ID() == id {
			return m, nil
		}
	}
	return nil, errors.WithStack(port.ErrNotFound)
}

func (s *enforcerProviderStore) ListLLMModels(context.Context, model.OrgID) ([]model.LLMModel, error) {
	s.listCalls++
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.models, nil
}

// enforcerOrgStore reports a fixed membership.
type enforcerOrgStore struct {
	port.OrgStore
	members int64
}

func (s *enforcerOrgStore) ListOrgMembers(context.Context, model.OrgID, port.ListOrgMembersOptions) ([]model.Membership, int64, error) {
	return nil, s.members, nil
}

// scopedUsageStore answers the plan-wide sums from per-model value totals, so
// a test can tell a provider-wide count from a per-model one.
type scopedUsageStore struct {
	port.UsageStore

	value map[model.LLMModelID]int64
	// scopes records the scope of every plan-wide sum.
	scopes []model.PlanUsageScope
}

func (s *scopedUsageStore) SumPlanUsageSince(_ context.Context, scope model.PlanUsageScope, _ time.Time) (int64, int64, error) {
	s.scopes = append(s.scopes, scope)
	var total int64
	for id, v := range s.value {
		if len(scope.ModelIDs) == 0 || slices.Contains(scope.ModelIDs, id) {
			total += v
		}
	}
	return 0, total, nil
}

type enforcerFixture struct {
	enforcer  *XoloSubscriptionEnforcer
	state     *SubscriptionStateDetailed
	usage     *scopedUsageStore
	providers *enforcerProviderStore
	kimi      model.LLMModel
	kimiFast  model.LLMModel // same upstream model as kimi, another proxy name
	glm       model.LLMModel
}

func newEnforcerFixture(t *testing.T, constraints ...model.PlanConstraint) *enforcerFixture {
	t.Helper()

	p := model.NewProvider("org-1", "OpenCode Go", "openai", "https://opencode.ai/zen/go/v1", "key", "USD")
	p.SetBillingMode(model.BillingModeSubscription)
	p.SetSubscriptionPlan(&model.SubscriptionPlan{Label: "Go", Constraints: constraints})

	f := &enforcerFixture{
		state:    NewSubscriptionState(),
		usage:    &scopedUsageStore{value: map[model.LLMModelID]int64{}},
		kimi:     model.NewLLMModel(p.ID(), "org-1", "kimi", "kimi-k3", "", 0, 0),
		kimiFast: model.NewLLMModel(p.ID(), "org-1", "kimi-fast", "kimi-k3", "", 0, 0),
		glm:      model.NewLLMModel(p.ID(), "org-1", "glm", "glm-5.3-flash", "", 0, 0),
	}
	f.providers = &enforcerProviderStore{provider: p, models: []model.LLMModel{f.kimi, f.kimiFast, f.glm}}
	f.enforcer = NewXoloSubscriptionEnforcer(f.providers, f.usage, service.NewFairShareService(f.usage), f.state, &enforcerOrgStore{})
	return f
}

func (f *enforcerFixture) request(m model.LLMModel) *genaiProxy.ProxyRequest {
	return &genaiProxy.ProxyRequest{
		Model: "org/" + m.ProxyName(),
		Metadata: map[string]any{
			MetaOrgID:   "org-1",
			MetaModelID: string(m.ID()),
		},
	}
}

// denial runs the pre-request check and returns the denial message, or "".
func (f *enforcerFixture) denial(t *testing.T, m model.LLMModel) string {
	t.Helper()
	res, err := f.enforcer.PreRequest(context.Background(), f.request(m))
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	if res == nil || res.Response == nil {
		return ""
	}
	body := res.Response.Body.(map[string]any)["error"].(map[string]any)
	return body["message"].(string)
}

func perModelConstraint(label string, value int64, overrides map[string]model.PlanBudget) model.PlanConstraint {
	return model.PlanConstraint{
		Kind:         model.ConstraintRollingWindow,
		Label:        label,
		Duration:     model.PlanDuration(5 * time.Hour),
		ValueBudget:  &value,
		Scope:        model.PlanScopeModel,
		ModelBudgets: overrides,
	}
}

func int64Ptr(v int64) *int64 { return &v }

func TestSubscriptionEnforcer_PerModelConstraintMetersEachModelOnItsOwn(t *testing.T) {
	f := newEnforcerFixture(t, perModelConstraint("5h", 3_000_000, nil))
	f.usage.value[f.kimi.ID()] = 2_000_000
	f.usage.value[f.kimiFast.ID()] = 1_000_000 // same upstream model: counted with kimi
	f.usage.value[f.glm.ID()] = 500_000

	msg := f.denial(t, f.kimi)
	if msg == "" {
		t.Fatal("kimi granted, want denied: its upstream model used the whole budget across both proxy names")
	}
	if !strings.Contains(msg, "5h · kimi-k3") {
		t.Errorf("message = %q, want it to name the constraint and the upstream model", msg)
	}

	if msg := f.denial(t, f.glm); msg != "" {
		t.Errorf("glm denied (%q), want granted: the other model's consumption is not its own", msg)
	}
}

func TestSubscriptionEnforcer_ModelBudgetOverridesTheConstraintBudget(t *testing.T) {
	f := newEnforcerFixture(t, perModelConstraint("5h", 3_000_000, map[string]model.PlanBudget{
		"glm-5.3-flash": {ValueBudget: int64Ptr(12_000_000)},
		"kimi-k3":       {}, // unlimited
	}))
	f.usage.value[f.kimi.ID()] = 50_000_000
	f.usage.value[f.glm.ID()] = 5_000_000

	if msg := f.denial(t, f.glm); msg != "" {
		t.Errorf("glm denied (%q), want granted under its own 12$ budget", msg)
	}
	if msg := f.denial(t, f.kimi); msg != "" {
		t.Errorf("kimi denied (%q), want granted: an override without budget leaves the model unlimited", msg)
	}

	f.usage.value[f.glm.ID()] = 12_000_000
	if msg := f.denial(t, f.glm); msg == "" {
		t.Error("glm granted, want denied once its own budget is spent")
	}
}

func TestSubscriptionEnforcer_ProviderConstraintStillCountsEveryModel(t *testing.T) {
	providerWide := model.PlanConstraint{
		Kind:        model.ConstraintRollingWindow,
		Label:       "month",
		Duration:    model.PlanDuration(720 * time.Hour),
		ValueBudget: int64Ptr(10_000_000),
	}
	f := newEnforcerFixture(t, perModelConstraint("5h", 3_000_000, nil), providerWide)
	f.usage.value[f.kimi.ID()] = 2_000_000
	f.usage.value[f.glm.ID()] = 8_000_000 // over its own 5h budget, and fills the month

	msg := f.denial(t, f.kimi)
	if !strings.Contains(msg, "[month]") {
		t.Errorf("message = %q, want kimi denied by the provider-wide month", msg)
	}
	for _, scope := range f.usage.scopes {
		if len(scope.ModelIDs) == 0 {
			return
		}
	}
	t.Error("no provider-wide sum was made for the provider-wide constraint")
}

func TestSubscriptionEnforcer_Upstream429CoolsDownOnlyTheRequestedModel(t *testing.T) {
	f := newEnforcerFixture(t, perModelConstraint("5h", 3_000_000, nil))

	upstream := &llm.HTTPError{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"120"}}}
	if _, err := f.enforcer.OnError(context.Background(), f.request(f.kimi), errors.WithStack(stderrors.Join(llm.ErrRateLimit, upstream))); err != nil {
		t.Fatalf("OnError: %v", err)
	}

	if msg := f.denial(t, f.kimiFast); !strings.Contains(msg, "cooldown") {
		t.Errorf("kimi-fast message = %q, want a cooldown: it targets the exhausted upstream model", msg)
	}
	if msg := f.denial(t, f.glm); msg != "" {
		t.Errorf("glm denied (%q), want granted: only kimi-k3 was exhausted", msg)
	}
}

func TestSubscriptionEnforcer_Upstream429CoolsDownAProviderWidePlan(t *testing.T) {
	f := newEnforcerFixture(t, tokenConstraint(1_000_000))

	upstream := &llm.HTTPError{StatusCode: http.StatusTooManyRequests}
	if _, err := f.enforcer.OnError(context.Background(), f.request(f.kimi), upstream); err != nil {
		t.Fatalf("OnError: %v", err)
	}

	if msg := f.denial(t, f.glm); !strings.Contains(msg, "cooldown") {
		t.Errorf("glm message = %q, want a cooldown: the plan meters the provider as a whole", msg)
	}
}

func TestSubscriptionEnforcer_Upstream429WithoutRetryAfterUsesTheDefaultCooldown(t *testing.T) {
	f := newEnforcerFixture(t, perModelConstraint("5h", 3_000_000, nil))

	before := time.Now()
	upstream := &llm.HTTPError{StatusCode: http.StatusTooManyRequests}
	if _, err := f.enforcer.OnError(context.Background(), f.request(f.kimi), upstream); err != nil {
		t.Fatalf("OnError: %v", err)
	}

	until, ok := f.state.exhaustedUntil[constraintKey{scopeKey{"org-1", f.kimi.ProviderID()}, "5h", "kimi-k3"}]
	if !ok {
		t.Fatal("no cooldown recorded for kimi-k3")
	}
	if got := until.Sub(before); got < defaultExhaustionCooldown || got > defaultExhaustionCooldown+time.Minute {
		t.Errorf("cooldown = %v, want about %v", got, defaultExhaustionCooldown)
	}
}

func TestSubscriptionEnforcer_UpstreamGroupingIsCached(t *testing.T) {
	f := newEnforcerFixture(t, perModelConstraint("5h", 3_000_000, nil))

	for range 3 {
		f.denial(t, f.kimi)
	}
	if f.providers.listCalls != 1 {
		t.Errorf("ListLLMModels called %d times, want 1: the grouping is reused within its TTL", f.providers.listCalls)
	}
}

func TestSubscriptionEnforcer_GroupingFailureMetersTheRequestedModelAlone(t *testing.T) {
	f := newEnforcerFixture(t, perModelConstraint("5h", 3_000_000, nil))
	f.providers.listErr = errors.New("store down")
	f.usage.value[f.kimi.ID()] = 1_000_000
	f.usage.value[f.kimiFast.ID()] = 2_000_000 // invisible without the grouping

	if msg := f.denial(t, f.kimi); msg != "" {
		t.Errorf("kimi denied (%q), want it metered on its own usage only", msg)
	}

	// The failure is not cached: the next request lists the models again.
	f.providers.listErr = nil
	if msg := f.denial(t, f.kimi); msg == "" {
		t.Error("kimi granted, want denied once the grouping is readable again")
	}
}

func TestSubscriptionState_SweepsExpiredCooldowns(t *testing.T) {
	s := NewSubscriptionState()
	past := time.Now().Add(-time.Minute)
	for i := range minPurgeAt {
		s.MarkExhausted("org", "prov", "5h", strconv.Itoa(i), past)
	}
	s.MarkExhausted("org", "prov", "5h", "live", time.Now().Add(time.Hour))

	if len(s.exhaustedUntil) != 1 {
		t.Errorf("%d cooldowns kept, want only the live one", len(s.exhaustedUntil))
	}
	if !s.IsExhausted("org", "prov", "5h", "live") {
		t.Error("the live cooldown was swept")
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{"absent", "", 0, false},
		{"seconds", "90", 90 * time.Second, true},
		{"zero", "0", 0, false},
		{"http date", now.Add(2 * time.Hour).Format(http.TimeFormat), 2 * time.Hour, true},
		{"past date", now.Add(-time.Hour).Format(http.TimeFormat), 0, false},
		{"garbage", "soon", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			if tc.value != "" {
				header.Set("Retry-After", tc.value)
			}
			got, ok := retryAfter(header, now)
			if got != tc.want || ok != tc.ok {
				t.Errorf("retryAfter(%q) = %v, %v; want %v, %v", tc.value, got, ok, tc.want, tc.ok)
			}
		})
	}
}
