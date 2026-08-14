package proxy

import (
	"context"
	"testing"

	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// fakeProviderStore embeds the port.ProviderStore interface (nil) and only
// overrides the two methods PostResponse actually calls.
type fakeProviderStore struct {
	port.ProviderStore
	provider model.Provider
	llmModel model.LLMModel
}

func (f *fakeProviderStore) GetProviderByID(ctx context.Context, id model.ProviderID) (model.Provider, error) {
	return f.provider, nil
}

func (f *fakeProviderStore) GetLLMModelByID(ctx context.Context, id model.LLMModelID) (model.LLMModel, error) {
	return f.llmModel, nil
}

// fakeOrgStore embeds the port.OrgStore interface (nil) and only overrides
// the method PostResponse actually calls.
type fakeOrgStore struct {
	port.OrgStore
	org model.Organization
}

func (f *fakeOrgStore) GetOrgByID(ctx context.Context, id model.OrgID) (model.Organization, error) {
	return f.org, nil
}

// fakeUsageStore embeds the port.UsageStore interface (nil) and only
// overrides RecordUsage, which is the only method PostResponse calls.
type fakeUsageStore struct {
	port.UsageStore
	recorded model.UsageRecord
}

func (f *fakeUsageStore) RecordUsage(ctx context.Context, record model.UsageRecord) error {
	f.recorded = record
	return nil
}

func newTestTracker(providerStore *fakeProviderStore, orgStore *fakeOrgStore) (*XoloUsageTracker, *fakeUsageStore) {
	usageStore := &fakeUsageStore{}
	exchangeRateService := service.NewExchangeRateService(nil, nil, 0)
	tracker := NewXoloUsageTracker(usageStore, providerStore, orgStore, exchangeRateService)
	return tracker, usageStore
}

func newTestRequestResponse(modelID model.LLMModelID, orgID model.OrgID, tokensUsed *genaiProxy.TokenUsage) (*genaiProxy.ProxyRequest, *genaiProxy.ProxyResponse) {
	req := &genaiProxy.ProxyRequest{
		UserID: "user-1",
		Model:  "cadoles/test-model",
		Metadata: map[string]any{
			MetaOrgID:   string(orgID),
			MetaModelID: string(modelID),
		},
	}
	res := &genaiProxy.ProxyResponse{TokensUsed: tokensUsed}
	return req, res
}

func TestUsageTrackerUsesProviderCostWhenAvailable(t *testing.T) {
	orgID := model.OrgID("org-1")
	providerID := model.NewProviderID()
	provider := model.NewProvider(orgID, "openrouter", "openrouter", "https://openrouter.ai", "key", "USD")
	llmModel := model.NewLLMModel(providerID, orgID, "cadoles/test-model", "real/model", "desc", 1000, 2000)
	org := model.NewOrganization(testTenantID, "org-1", "Org 1", "", "USD")

	tracker, usageStore := newTestTracker(&fakeProviderStore{provider: provider, llmModel: llmModel}, &fakeOrgStore{org: org})

	cost := 0.0123 // USD, as reported by OpenRouter
	req, res := newTestRequestResponse(llmModel.ID(), orgID, &genaiProxy.TokenUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		Cost:             &cost,
		CostCurrency:     "USD",
	})

	if _, err := tracker.PostResponse(context.Background(), req, res); err != nil {
		t.Fatalf("PostResponse() error = %v", err)
	}

	if usageStore.recorded == nil {
		t.Fatal("expected a usage record to be persisted")
	}
	if got, want := usageStore.recorded.Cost(), int64(12300); got != want {
		t.Errorf("Cost() = %d, want %d (0.0123 USD in microcents)", got, want)
	}
	if got, want := usageStore.recorded.CostSource(), model.CostSourceProvider; got != want {
		t.Errorf("CostSource() = %q, want %q", got, want)
	}
}

func TestUsageTrackerFallsBackToComputedCost(t *testing.T) {
	orgID := model.OrgID("org-1")
	providerID := model.NewProviderID()
	provider := model.NewProvider(orgID, "openai", "openai", "https://api.openai.com", "key", "USD")
	// 1000 microcents/1K prompt tokens, 2000 microcents/1K completion tokens
	llmModel := model.NewLLMModel(providerID, orgID, "cadoles/test-model", "real/model", "desc", 1000, 2000)
	org := model.NewOrganization(testTenantID, "org-1", "Org 1", "", "USD")

	tracker, usageStore := newTestTracker(&fakeProviderStore{provider: provider, llmModel: llmModel}, &fakeOrgStore{org: org})

	req, res := newTestRequestResponse(llmModel.ID(), orgID, &genaiProxy.TokenUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		// No Cost reported (OpenAI does not expose it).
	})

	if _, err := tracker.PostResponse(context.Background(), req, res); err != nil {
		t.Fatalf("PostResponse() error = %v", err)
	}

	if usageStore.recorded == nil {
		t.Fatal("expected a usage record to be persisted")
	}
	// (1000 * 1000 / 1000) + (500 * 2000 / 1000) = 1000 + 1000 = 2000
	if got, want := usageStore.recorded.Cost(), int64(2000); got != want {
		t.Errorf("Cost() = %d, want %d", got, want)
	}
	if got, want := usageStore.recorded.CostSource(), model.CostSourceComputed; got != want {
		t.Errorf("CostSource() = %q, want %q", got, want)
	}
}

// testTenantID is the tenant every fixture of this package belongs to.
// Tenancy is not what these tests exercise: they only need a stable, shared
// owner so the tenant-scoped unique keys behave like the pre-tenant ones.
const testTenantID = model.TenantID("test-tenant")
