package proxy

import (
	"context"
	"strings"
	"testing"
	"time"

	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

// unknownModelStore answers every lookup with ErrNotFound, as the store does
// for a virtual model name: it has no row in the LLM model table.
type unknownModelStore struct {
	port.ProviderStore
}

func (s *unknownModelStore) GetLLMModelByID(context.Context, model.LLMModelID) (model.LLMModel, error) {
	return nil, port.ErrNotFound
}

func (s *unknownModelStore) GetLLMModelByProxyName(context.Context, model.OrgID, string) (model.LLMModel, error) {
	return nil, port.ErrNotFound
}

// exhaustedEnforcer builds an enforcer whose user has spent its whole daily budget.
// The resolver is wired with no org quota on file so the org-wide branch (the
// one that consumes OrgQuota from the second resolver return value) reaches
// the "no org cap" branch without trying to read a quota store.
func exhaustedEnforcer(providerStore port.ProviderStore) *XoloQuotaEnforcer {
	usage := &fakeUsage{spent: map[time.Time]int64{
		model.StartOfDay(time.Now()): 1_000_000,
	}}
	resolver := fakeQuotaResolver{quota: &model.EffectiveQuota{
		DailyBudget: i64(1_000),
		Currency:    "EUR",
	}}
	return NewXoloQuotaEnforcer(resolver, usage, providerStore)
}

func quotaTestRequest(exec *pipeline.ForwardExecution) *genaiProxy.ProxyRequest {
	req := &genaiProxy.ProxyRequest{
		UserID:   "user-1",
		Model:    "acme/support-agent",
		Metadata: map[string]any{MetaOrgID: "org-1"},
	}
	if exec != nil {
		req.Metadata[metaPipelineExecution] = exec
	}
	return req
}

// A pipeline whose terminal node forges its own response never reaches a
// provider: an exhausted budget must not turn that answer into a 429.
func TestQuotaEnforcerSkipsPipelineAnswerWithoutProvider(t *testing.T) {
	enforcer := exhaustedEnforcer(&unknownModelStore{})
	exec := &pipeline.ForwardExecution{
		ResolvedClient: NewDummyLLMClient("Je ne réponds qu'aux questions sur le service.", "dummy"),
		ResolvedModel:  "dummy",
	}

	result, err := enforcer.PreRequest(context.Background(), quotaTestRequest(exec))
	if err != nil {
		t.Fatalf("PreRequest() error = %v", err)
	}
	if result != nil {
		t.Fatalf("expected the request to pass, got response %+v", result.Response)
	}
}

// A pipeline that resolved a real model stays under quota enforcement.
func TestQuotaEnforcerEnforcesPipelineAnswerFromProvider(t *testing.T) {
	orgID := model.OrgID("org-1")
	providerID := model.NewProviderID()
	llmModel := model.NewLLMModel(providerID, orgID, "acme/qwen-strong", "qwen", "desc", 1000, 2000)
	provider := model.NewProvider(orgID, "ollama", "openai", "http://localhost:11434/v1", "key", "EUR")

	enforcer := exhaustedEnforcer(&fakeProviderStore{provider: provider, llmModel: llmModel})
	req := quotaTestRequest(&pipeline.ForwardExecution{
		ResolvedClient:  NewDummyLLMClient("hello", "qwen"),
		ResolvedModel:   "qwen",
		ResolvedModelID: llmModel.ID(),
	})
	req.Metadata[MetaModelID] = string(llmModel.ID())

	result, err := enforcer.PreRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("PreRequest() error = %v", err)
	}
	if result == nil || result.Response == nil || result.Response.StatusCode != 429 {
		t.Fatalf("expected a 429 quota response, got %+v", result)
	}
	// Pin the user-scope message subject so the new "User" prefix cannot
	// silently regress to "Daily budget exceeded" without the test suite noticing.
	body, _ := result.Response.Body.(map[string]any)
	errBody, _ := body["error"].(map[string]any)
	msg, _ := errBody["message"].(string)
	if !strings.Contains(msg, "User daily budget exceeded") {
		t.Errorf("expected user daily budget error, got %q", msg)
	}
}

// An application token carries an ApplicationID in metadata. A request whose
// application counter exceeds the application budget must be rejected even
// though the shadow user has no budget of its own: this is the gap issue #64
// describes. Without the application check the request would silently slip
// through to the org budget.
//
// The per-user check is intentionally skipped on this path: the shadow user's
// counter is never fed for an application record, and resolving it would cost
// an extra GetOrgByID + ListOrgMembers when ShareQuotaEqually is on. The
// countingResolver below asserts that ResolveEffectiveQuota is not called at
// all for an application request.
//
// The resolver simulates "no org quota on file" for the org-wide branch by
// returning nil as the org quota — the same shape the real resolvers return
// when their GetQuota on QuotaScopeOrg hits ErrNotFound. Without nil the
// test would not exercise the org-wide branch at all (the enforcer would skip).
func TestQuotaEnforcerEnforcesApplicationQuota(t *testing.T) {
	orgID := model.OrgID("org-1")
	providerID := model.NewProviderID()
	llmModel := model.NewLLMModel(providerID, orgID, "acme/qwen-strong", "qwen", "desc", 1000, 2000)
	provider := model.NewProvider(orgID, "ollama", "openai", "http://localhost:11434/v1", "key", "EUR")

	// The application counter is what we expect to trip. The user-scope
	// resolver is wired to the counting wrapper so we can assert it is
	// never called for an application request — the shadow user is a
	// routing artifact that has no budget of its own.
	now := time.Now()
	usage := &fakeUsage{spent: map[time.Time]int64{
		model.StartOfDay(now): 0, // user scope: nothing spent (the shadow user is not a counter)
	}}
	counting := &countingQuotaResolver{
		appQuotaResolver: appQuotaResolver{
			userQuota: &model.EffectiveQuota{Currency: "EUR"}, // no user budget
			appQuota:  &model.EffectiveQuota{DailyBudget: i64(60_000), Currency: "EUR"},
			orgQuota:  nil, // no org quota on file: org-wide branch skips
		},
	}
	enforcer := NewXoloQuotaEnforcer(counting, usage, &fakeProviderStore{provider: provider, llmModel: llmModel})

	req := quotaTestRequest(&pipeline.ForwardExecution{
		ResolvedClient:  NewDummyLLMClient("hello", "qwen"),
		ResolvedModel:   "qwen",
		ResolvedModelID: llmModel.ID(),
	})
	req.Metadata[MetaModelID] = string(llmModel.ID())
	req.Metadata[MetaApplicationID] = "app-acme-ci"

	// First call: nothing spent yet, the application check passes.
	result, err := enforcer.PreRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("PreRequest() error = %v", err)
	}
	if result != nil {
		t.Fatalf("expected the request to pass, got response %+v", result.Response)
	}
	if counting.userCalls != 0 {
		t.Errorf("ResolveEffectiveQuota was called %d time(s) for an application request, want 0", counting.userCalls)
	}
	if counting.appCalls != 1 {
		t.Errorf("ResolveEffectiveQuotaForApplication was called %d time(s), want 1", counting.appCalls)
	}

	// Application counter has now reached the budget: the next request is rejected.
	usage.spent[model.StartOfDay(now)] = 60_000
	result, err = enforcer.PreRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("PreRequest() error = %v", err)
	}
	if result == nil || result.Response == nil || result.Response.StatusCode != 429 {
		t.Fatalf("expected a 429 application quota response, got %+v", result)
	}
	body, _ := result.Response.Body.(map[string]any)
	errBody, _ := body["error"].(map[string]any)
	if msg, _ := errBody["message"].(string); msg == "" || !strings.Contains(msg, "Application daily budget exceeded") {
		t.Errorf("expected application daily budget error, got %q", msg)
	}
	// The 429 path took the same branch as the passing call: the user-scope
	// resolver must still be skipped and the application resolver consulted.
	// Re-asserting the counters after the rejection pinpoints a regression
	// that flips the branch only on the rejection path (e.g. a fall-through
	// to user-scope on error).
	if counting.userCalls != 0 {
		t.Errorf("ResolveEffectiveQuota was called %d time(s) on the 429 path, want 0", counting.userCalls)
	}
	if counting.appCalls != 2 {
		t.Errorf("ResolveEffectiveQuotaForApplication was called %d time(s) after the 429, want 2", counting.appCalls)
	}
}

// countingQuotaResolver wraps appQuotaResolver to count how many times each
// method is called. The application path must invoke the application resolver
// exactly once and the user resolver never.
type countingQuotaResolver struct {
	appQuotaResolver
	userCalls int
	appCalls  int
}

func (r *countingQuotaResolver) ResolveEffectiveQuota(ctx context.Context, u model.UserID, o model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	r.userCalls++
	return r.appQuotaResolver.ResolveEffectiveQuota(ctx, u, o)
}

func (r *countingQuotaResolver) ResolveEffectiveQuotaForApplication(ctx context.Context, a model.ApplicationID, o model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	r.appCalls++
	return r.appQuotaResolver.ResolveEffectiveQuotaForApplication(ctx, a, o)
}

// An application budget that is set only on the monthly or yearly periods
// must still trigger the right branch — the daily coverage above does not
// exercise the StartOfMonth / StartOfYear lookups, the error messages or the
// currency plumbing.
func TestQuotaEnforcerEnforcesApplicationQuotaMonthlyAndYearly(t *testing.T) {
	orgID := model.OrgID("org-1")
	providerID := model.NewProviderID()
	llmModel := model.NewLLMModel(providerID, orgID, "acme/qwen-strong", "qwen", "desc", 1000, 2000)
	provider := model.NewProvider(orgID, "ollama", "openai", "http://localhost:11434/v1", "key", "EUR")

	now := time.Now()
	baseReq := func() *genaiProxy.ProxyRequest {
		req := quotaTestRequest(&pipeline.ForwardExecution{
			ResolvedClient:  NewDummyLLMClient("hello", "qwen"),
			ResolvedModel:   "qwen",
			ResolvedModelID: llmModel.ID(),
		})
		req.Metadata[MetaModelID] = string(llmModel.ID())
		req.Metadata[MetaApplicationID] = "app-acme-ci"
		return req
	}

	tests := []struct {
		name           string
		spent          map[time.Time]int64
		appQuota       *model.EffectiveQuota
		wantStatusCode int
		wantMessage    string
	}{
		{
			name: "monthly budget exhausted",
			spent: map[time.Time]int64{
				model.StartOfMonth(now): 1_500_000,
			},
			appQuota:       &model.EffectiveQuota{MonthlyBudget: i64(1_500_000), Currency: "EUR"},
			wantStatusCode: 429,
			wantMessage:    "Application monthly budget exceeded",
		},
		{
			name: "yearly budget exhausted",
			spent: map[time.Time]int64{
				model.StartOfYear(now): 20_000_000,
			},
			appQuota:       &model.EffectiveQuota{YearlyBudget: i64(20_000_000), Currency: "EUR"},
			wantStatusCode: 429,
			wantMessage:    "Application yearly budget exceeded",
		},
		{
			name:  "monthly and yearly below budget: passes",
			spent: map[time.Time]int64{model.StartOfMonth(now): 0, model.StartOfYear(now): 0},
			appQuota: &model.EffectiveQuota{
				MonthlyBudget: i64(1_500_000),
				YearlyBudget:  i64(20_000_000),
				Currency:      "EUR",
			},
			wantStatusCode: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			enforcer := NewXoloQuotaEnforcer(
				appQuotaResolver{
					userQuota: &model.EffectiveQuota{Currency: "EUR"},
					appQuota:  tc.appQuota,
					orgQuota:  nil, // no org quota on file
				},
				&fakeUsage{spent: tc.spent},
				&fakeProviderStore{provider: provider, llmModel: llmModel},
			)

			result, err := enforcer.PreRequest(context.Background(), baseReq())
			if err != nil {
				t.Fatalf("PreRequest() error = %v", err)
			}
			if tc.wantStatusCode == 0 {
				if result != nil {
					t.Fatalf("expected the request to pass, got %+v", result.Response)
				}
				return
			}
			if result == nil || result.Response == nil || result.Response.StatusCode != tc.wantStatusCode {
				t.Fatalf("expected status %d, got %+v", tc.wantStatusCode, result)
			}
			body, _ := result.Response.Body.(map[string]any)
			errBody, _ := body["error"].(map[string]any)
			msg, _ := errBody["message"].(string)
			if !strings.Contains(msg, tc.wantMessage) {
				t.Errorf("expected error %q, got %q", tc.wantMessage, msg)
			}
		})
	}
}

// Without an ApplicationID in metadata the application path must not run at
// all, even when the resolver would return a non-nil application quota. The
// shadow user's user-scope check still applies as before.
func TestQuotaEnforcerSkipsApplicationPathWithoutAppID(t *testing.T) {
	orgID := model.OrgID("org-1")
	providerID := model.NewProviderID()
	llmModel := model.NewLLMModel(providerID, orgID, "acme/qwen-strong", "qwen", "desc", 1000, 2000)
	provider := model.NewProvider(orgID, "ollama", "openai", "http://localhost:11434/v1", "key", "EUR")

	enforcer := NewXoloQuotaEnforcer(
		appQuotaResolver{
			userQuota: &model.EffectiveQuota{DailyBudget: i64(1_000), Currency: "EUR"},
			orgQuota:  nil,
		},
		&fakeUsage{spent: map[time.Time]int64{model.StartOfDay(time.Now()): 0}},
		&fakeProviderStore{provider: provider, llmModel: llmModel},
	)

	req := quotaTestRequest(&pipeline.ForwardExecution{
		ResolvedClient:  NewDummyLLMClient("hello", "qwen"),
		ResolvedModel:   "qwen",
		ResolvedModelID: llmModel.ID(),
	})
	req.Metadata[MetaModelID] = string(llmModel.ID())
	// Deliberately no MetaApplicationID.

	result, err := enforcer.PreRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("PreRequest() error = %v", err)
	}
	if result != nil {
		t.Fatalf("expected the request to pass without an app id, got %+v", result.Response)
	}
}

// Application budget under the cap, organisation budget exhausted: the
// org-wide branch must still trip on the application path. The application
// resolver is called first and succeeds; the org-wide branch then reads the
// org quota from the resolver's second return value and rejects. This is the
// mirror of the existing TestQuotaEnforcerEnforcesApplicationQuota, which
// set orgQuota: nil to skip this branch entirely — the gap a future guard
// on appID=="" around the org-wide check would silently reopen.
func TestQuotaEnforcerEnforcesOrgBudgetWhenAppUnderLimit(t *testing.T) {
	orgID := model.OrgID("org-1")
	providerID := model.NewProviderID()
	llmModel := model.NewLLMModel(providerID, orgID, "acme/qwen-strong", "qwen", "desc", 1000, 2000)
	provider := model.NewProvider(orgID, "ollama", "openai", "http://localhost:11434/v1", "key", "EUR")

	now := time.Now()
	// Org-wide daily budget at zero: any non-negative spend exceeds it.
	orgDaily := int64(0)
	counting := &countingQuotaResolver{
		appQuotaResolver: appQuotaResolver{
			// Application budget is generous; the request stays under it.
			appQuota: &model.EffectiveQuota{DailyBudget: i64(1_000_000), Currency: "EUR"},
			orgQuota: &fakeQuota{scope: model.QuotaScopeOrg, currency: "EUR", daily: &orgDaily},
		},
	}
	enforcer := NewXoloQuotaEnforcer(
		counting,
		&fakeUsage{spent: map[time.Time]int64{model.StartOfDay(now): 1_000}},
		&fakeProviderStore{provider: provider, llmModel: llmModel},
	)

	req := quotaTestRequest(&pipeline.ForwardExecution{
		ResolvedClient:  NewDummyLLMClient("hello", "qwen"),
		ResolvedModel:   "qwen",
		ResolvedModelID: llmModel.ID(),
	})
	req.Metadata[MetaModelID] = string(llmModel.ID())
	req.Metadata[MetaApplicationID] = "app-acme-ci"

	result, err := enforcer.PreRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("PreRequest() error = %v", err)
	}
	if result == nil || result.Response == nil || result.Response.StatusCode != 429 {
		t.Fatalf("expected a 429 organisation quota response, got %+v", result)
	}
	body, _ := result.Response.Body.(map[string]any)
	errBody, _ := body["error"].(map[string]any)
	if msg, _ := errBody["message"].(string); msg == "" || !strings.Contains(msg, "Organization daily budget exceeded") {
		t.Errorf("expected organisation daily budget error, got %q", msg)
	}
	// The application resolver was consulted before the org branch tripped.
	if counting.appCalls != 1 {
		t.Errorf("ResolveEffectiveQuotaForApplication was called %d time(s), want 1", counting.appCalls)
	}
	// The org-wide branch tripped, but the application branch was first: the
	// user-scope resolver must still be skipped. A regression that flips
	// the order — or silently consults the user-scope resolver on the
	// application path — would slip past without this assertion.
	if counting.userCalls != 0 {
		t.Errorf("ResolveEffectiveQuota was called %d time(s) on the application path, want 0", counting.userCalls)
	}
}

// Both the application and the organisation budgets are exhausted at once.
// The application-scope check runs first inside PreRequest and returns its
// 429; the organisation branch never executes. Pin the ordering so a future
// refactor cannot swap the two error messages without the test suite noticing.
func TestQuotaEnforcerPrefersApplicationErrorWhenBothExhausted(t *testing.T) {
	orgID := model.OrgID("org-1")
	providerID := model.NewProviderID()
	llmModel := model.NewLLMModel(providerID, orgID, "acme/qwen-strong", "qwen", "desc", 1000, 2000)
	provider := model.NewProvider(orgID, "ollama", "openai", "http://localhost:11434/v1", "key", "EUR")

	now := time.Now()
	orgDaily := int64(0)
	counting := &countingQuotaResolver{
		appQuotaResolver: appQuotaResolver{
			// Application budget: 60 000 µ¢/day, primed to the cap.
			appQuota: &model.EffectiveQuota{DailyBudget: i64(60_000), Currency: "EUR"},
			// Org budget: also exhausted, but the application branch should
			// trip first and we never reach the org check.
			orgQuota: &fakeQuota{scope: model.QuotaScopeOrg, currency: "EUR", daily: &orgDaily},
		},
	}
	usage := &fakeUsage{spent: map[time.Time]int64{
		// Application counter exactly at the cap; the org counter would also
		// be over its own cap if the enforcer ever reached that branch.
		model.StartOfDay(now): 60_000,
	}}
	enforcer := NewXoloQuotaEnforcer(
		counting,
		usage,
		&fakeProviderStore{provider: provider, llmModel: llmModel},
	)

	req := quotaTestRequest(&pipeline.ForwardExecution{
		ResolvedClient:  NewDummyLLMClient("hello", "qwen"),
		ResolvedModel:   "qwen",
		ResolvedModelID: llmModel.ID(),
	})
	req.Metadata[MetaModelID] = string(llmModel.ID())
	req.Metadata[MetaApplicationID] = "app-acme-ci"

	result, err := enforcer.PreRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("PreRequest() error = %v", err)
	}
	if result == nil || result.Response == nil || result.Response.StatusCode != 429 {
		t.Fatalf("expected a 429, got %+v", result)
	}
	body, _ := result.Response.Body.(map[string]any)
	errBody, _ := body["error"].(map[string]any)
	msg, _ := errBody["message"].(string)
	if !strings.Contains(msg, "Application daily budget exceeded") {
		t.Errorf("expected application daily budget error, got %q", msg)
	}
	if strings.Contains(msg, "Organization") {
		t.Errorf("organisation message leaked even though the application check ran first: %q", msg)
	}
	// The application branch tripped, so the org branch should not have run.
	if counting.appCalls != 1 {
		t.Errorf("ResolveEffectiveQuotaForApplication was called %d time(s), want 1", counting.appCalls)
	}
	// The user-scope resolver must also be skipped on the application path
	// even when the org branch never runs: a fall-through to ResolveEffectiveQuota
	// would silently spend the share-by-members branch on a shadow user.
	if counting.userCalls != 0 {
		t.Errorf("ResolveEffectiveQuota was called %d time(s) on the application path, want 0", counting.userCalls)
	}
}

// appQuotaResolver separates the answers for user and application paths so the
// tests can exercise each independently. The two effective quotas are merged
// at call time; the per-principal fields exist purely so each test case can
// spell out what it expects.
//
// The resolver also carries the org quota that the org-wide branch in
// XoloQuotaEnforcer consumes (issue #82). Setting orgQuota simulates the
// contract the production resolvers honour; leaving it nil exercises the
// resolver-loaded-none-on-file path the org-wide branch skips.
type appQuotaResolver struct {
	userQuota *model.EffectiveQuota
	appQuota  *model.EffectiveQuota
	orgQuota  model.Quota
	err       error
}

func (r appQuotaResolver) ResolveEffectiveQuota(context.Context, model.UserID, model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	if r.err != nil {
		return nil, nil, r.err
	}
	return r.userQuota, r.orgQuota, nil
}

func (r appQuotaResolver) ResolveEffectiveQuotaForApplication(context.Context, model.ApplicationID, model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	if r.err != nil {
		return nil, nil, r.err
	}
	return r.appQuota, r.orgQuota, nil
}

// ResolveTeamQuotas sets no team budget.
func (r appQuotaResolver) ResolveTeamQuotas(context.Context, model.TeamID, model.UserID) (model.Quota, *model.EffectiveQuota, error) {
	return nil, nil, nil
}

// fakeQuota implements model.Quota with the minimum the enforcer reads.
type fakeQuota struct {
	scope    model.QuotaScope
	currency string
	daily    *int64
	monthly  *int64
	yearly   *int64
}

func (q *fakeQuota) ID() model.QuotaID       { return "q" }
func (q *fakeQuota) Scope() model.QuotaScope { return q.scope }
func (q *fakeQuota) ScopeID() string         { return "" }
func (q *fakeQuota) Currency() string        { return q.currency }
func (q *fakeQuota) DailyBudget() *int64     { return q.daily }
func (q *fakeQuota) MonthlyBudget() *int64   { return q.monthly }
func (q *fakeQuota) YearlyBudget() *int64    { return q.yearly }
func (q *fakeQuota) Cascade() model.QuotaCascade { return model.QuotaCascadeFree }
func (q *fakeQuota) CreatedAt() time.Time    { return time.Time{} }
func (q *fakeQuota) UpdatedAt() time.Time    { return time.Time{} }

var _ model.Quota = (*fakeQuota)(nil)

// ── End-to-end: real service.QuotaService wiring ───────────────────────────────────
//
// Issue #82 requires a test that goes through the production QuotaService
// rather than a stub resolver: a regression that drops the org quota from
// the resolver's second return value would slip past the unit tests that
// only inspect the enforcer's behaviour with the fakeQuotaResolver stub.
// This test wires a real service.QuotaService on top of a minimal
// endToEndQuotaStore, then drives the enforcer through an application
// request whose org-wide daily budget is exhausted: the org-wide block must
// trip on the org quota the resolver returns as its second value.

// endToEndQuotaStore is a minimal port.QuotaStore that returns configured
// records for org and application scopes. Anything else answers ErrNotFound,
// matching the production store's contract for an unset quota.
type endToEndQuotaStore struct {
	port.QuotaStore
	orgQuota model.Quota
	appQuota model.Quota
}

func (s *endToEndQuotaStore) GetQuota(_ context.Context, scope model.QuotaScope, _ string) (model.Quota, error) {
	switch scope {
	case model.QuotaScopeOrg:
		if s.orgQuota == nil {
			return nil, port.ErrNotFound
		}
		return s.orgQuota, nil
	case model.QuotaScopeApplication:
		if s.appQuota == nil {
			return nil, port.ErrNotFound
		}
		return s.appQuota, nil
	}
	return nil, port.ErrNotFound
}

func (s *endToEndQuotaStore) ResolveEffectiveQuota(context.Context, model.UserID, model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	return &model.EffectiveQuota{}, nil, nil
}

func (s *endToEndQuotaStore) ResolveEffectiveQuotaForApplication(context.Context, model.ApplicationID, model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	return &model.EffectiveQuota{}, nil, nil
}

// endToEndScopedUsage lets the end-to-end tests return different spent
// amounts for each scope. The default fakeUsage answers the same value for
// every scope, which is fine for the unit tests but cannot express the
// org-wide-tripping-with-application-passing shape the end-to-end tests need:
// the resolver merges min(app, org) into a single EffectiveQuota, so the
// application-scope check would trip on the same threshold as the org-wide
// branch. Splitting the counters per scope is what lets each branch see
// the spent amount it should see.
type endToEndScopedUsage struct {
	port.UsageStore
	byScope map[model.QuotaScope]int64
}

func (s *endToEndScopedUsage) SumQuotaCostSince(_ context.Context, scope model.QuotaScope, _ string, _ model.OrgID, _ time.Time) (int64, error) {
	return s.byScope[scope], nil
}

// endToEndOrgStore is a minimal service.OrgProvider: a single org with
// sharing disabled, no members. The service only ever queries one orgID in
// this test, so a fixed response is enough.
type endToEndOrgStore struct{}

func (endToEndOrgStore) GetOrgByID(_ context.Context, _ model.OrgID) (model.Organization, error) {
	return endToEndOrg{}, nil
}

func (endToEndOrgStore) ListOrgMembers(context.Context, model.OrgID, port.ListOrgMembersOptions) ([]model.Membership, int64, error) {
	return nil, 0, nil
}

// endToEndOrg implements model.Organization with sharing disabled. It does
// not need to be exhaustive — the resolver only calls ID, ShareQuotaEqually
// and Currency, the rest can be zero values.
type endToEndOrg struct{}

func (endToEndOrg) ID() model.OrgID          { return "" }
func (endToEndOrg) TenantID() model.TenantID { return "" }
func (endToEndOrg) Slug() string             { return "" }
func (endToEndOrg) Name() string             { return "" }
func (endToEndOrg) Description() string      { return "" }
func (endToEndOrg) Active() bool             { return true }
func (endToEndOrg) Currency() string         { return "EUR" }
func (endToEndOrg) CreatedAt() time.Time     { return time.Time{} }
func (endToEndOrg) UpdatedAt() time.Time     { return time.Time{} }
func (endToEndOrg) ShareQuotaEqually() bool  { return false }

var _ service.OrgProvider = endToEndOrgStore{}

// TestQuotaEnforcer_ApplicationOrgCap_ThroughRealQuotaService is the
// regression catch Bornholm asked for on issue #82. It exercises the
// production service.QuotaService through the enforcer on the application
// path: an application request whose org-wide daily budget is spent must
// get a 429 from the org-wide block. A regression that drops the second
// return value (the org quota) on ResolveEffectiveQuotaForApplication
// would make the enforcer skip the org-wide block silently, the request
// would return 200, and this test would fail loudly.
func TestQuotaEnforcer_ApplicationOrgCap_ThroughRealQuotaService(t *testing.T) {
	orgID := model.OrgID("org-1")
	providerID := model.NewProviderID()
	llmModel := model.NewLLMModel(providerID, orgID, "acme/qwen-strong", "qwen", "desc", 1000, 2000)
	provider := model.NewProvider(orgID, "ollama", "openai", "http://localhost:11434/v1", "key", "EUR")

	orgDaily := int64(1_000)
	store := &endToEndQuotaStore{
		orgQuota: &fakeQuota{scope: model.QuotaScopeOrg, currency: "EUR", daily: &orgDaily},
	}
	svc := service.NewQuotaService(store, endToEndOrgStore{})

	usage := &endToEndScopedUsage{
		byScope: map[model.QuotaScope]int64{
			// Application counter: nothing spent, the application check passes.
			model.QuotaScopeApplication: 0,
			// Org counter: spent above the cap, the org-wide branch must trip.
			model.QuotaScopeOrg: 5_000,
		},
	}
	enforcer := NewXoloQuotaEnforcer(svc, usage, &fakeProviderStore{provider: provider, llmModel: llmModel})

	req := quotaTestRequest(&pipeline.ForwardExecution{
		ResolvedClient:  NewDummyLLMClient("hello", "qwen"),
		ResolvedModel:   "qwen",
		ResolvedModelID: llmModel.ID(),
	})
	req.Metadata[MetaModelID] = string(llmModel.ID())
	req.Metadata[MetaApplicationID] = "app-acme-ci"

	result, err := enforcer.PreRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("PreRequest() error = %v", err)
	}
	if result == nil || result.Response == nil || result.Response.StatusCode != 429 {
		t.Fatalf("expected a 429 organisation quota response, got %+v", result)
	}
	body, _ := result.Response.Body.(map[string]any)
	errBody, _ := body["error"].(map[string]any)
	msg, _ := errBody["message"].(string)
	if !strings.Contains(msg, "Organization daily budget exceeded") {
		t.Errorf("expected organisation daily budget error, got %q", msg)
	}
}

// TestQuotaEnforcer_UserOrgCap_ThroughRealQuotaService is the user-path
// mirror: a regular user request that has spent its org's daily budget must
// get a 429 from the org-wide block. This pins the same contract on
// ResolveEffectiveQuota (the user resolver) as the application test pins
// on ResolveEffectiveQuotaForApplication.
func TestQuotaEnforcer_UserOrgCap_ThroughRealQuotaService(t *testing.T) {
	orgID := model.OrgID("org-1")
	providerID := model.NewProviderID()
	llmModel := model.NewLLMModel(providerID, orgID, "acme/qwen-strong", "qwen", "desc", 1000, 2000)
	provider := model.NewProvider(orgID, "ollama", "openai", "http://localhost:11434/v1", "key", "EUR")

	orgDaily := int64(1_000)
	store := &endToEndQuotaStore{
		orgQuota: &fakeQuota{scope: model.QuotaScopeOrg, currency: "EUR", daily: &orgDaily},
	}
	svc := service.NewQuotaService(store, endToEndOrgStore{})

	usage := &endToEndScopedUsage{
		byScope: map[model.QuotaScope]int64{
			// User counter: nothing spent, the user-scope check passes.
			model.QuotaScopeUser: 0,
			// Org counter: spent above the cap, the org-wide branch must trip.
			model.QuotaScopeOrg: 5_000,
		},
	}
	enforcer := NewXoloQuotaEnforcer(svc, usage, &fakeProviderStore{provider: provider, llmModel: llmModel})

	req := quotaTestRequest(&pipeline.ForwardExecution{
		ResolvedClient:  NewDummyLLMClient("hello", "qwen"),
		ResolvedModel:   "qwen",
		ResolvedModelID: llmModel.ID(),
	})
	req.Metadata[MetaModelID] = string(llmModel.ID())
	// No MetaApplicationID — this is the user path.

	result, err := enforcer.PreRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("PreRequest() error = %v", err)
	}
	if result == nil || result.Response == nil || result.Response.StatusCode != 429 {
		t.Fatalf("expected a 429 organisation quota response, got %+v", result)
	}
	body, _ := result.Response.Body.(map[string]any)
	errBody, _ := body["error"].(map[string]any)
	msg, _ := errBody["message"].(string)
	if !strings.Contains(msg, "Organization daily budget exceeded") {
		t.Errorf("expected organisation daily budget error, got %q", msg)
	}
}

// Issue #82 fail-closed sanity check. The contract is now structural
// (the resolver interface requires the second return value), so a resolver
// that forgets the org quota does not compile — but the contract must also
// keep the org-wide block from rejecting requests when no org quota is on
// file (the legitimate nil-org-quota path). This test exercises that
// branch end to end, through the production QuotaService.
func TestQuotaEnforcer_NoOrgQuotaOnFilePassesThroughQuotaService(t *testing.T) {
	orgID := model.OrgID("org-1")
	providerID := model.NewProviderID()
	llmModel := model.NewLLMModel(providerID, orgID, "acme/qwen-strong", "qwen", "desc", 1000, 2000)
	provider := model.NewProvider(orgID, "ollama", "openai", "http://localhost:11434/v1", "key", "EUR")

	// No org quota on file, no app quota on file: the org-wide block must
	// skip without error. The resolver returns nil for the org quota on
	// both methods and the enforcer must accept that.
	store := &endToEndQuotaStore{}
	svc := service.NewQuotaService(store, endToEndOrgStore{})

	usage := &endToEndScopedUsage{
		byScope: map[model.QuotaScope]int64{
			model.QuotaScopeApplication: 0,
			model.QuotaScopeOrg:         0,
		},
	}
	enforcer := NewXoloQuotaEnforcer(svc, usage, &fakeProviderStore{provider: provider, llmModel: llmModel})

	req := quotaTestRequest(&pipeline.ForwardExecution{
		ResolvedClient:  NewDummyLLMClient("hello", "qwen"),
		ResolvedModel:   "qwen",
		ResolvedModelID: llmModel.ID(),
	})
	req.Metadata[MetaModelID] = string(llmModel.ID())
	req.Metadata[MetaApplicationID] = "app-acme-ci"

	result, err := enforcer.PreRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("PreRequest() error = %v", err)
	}
	if result != nil {
		t.Fatalf("expected the request to pass with no org quota on file, got %+v", result.Response)
	}
}

// no further helpers.
