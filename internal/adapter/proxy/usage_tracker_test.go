package proxy

import (
	"context"
	"errors"
	"strings"
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

// interruptedFixture builds a tracker and a request/response pair for an
// interrupted stream, the shape shared by the tests below.
func interruptedFixture(t *testing.T, tokens *genaiProxy.TokenUsage, interruption *genaiProxy.StreamInterruption) (*fakeUsageStore, *genaiProxy.ProxyRequest, *genaiProxy.ProxyResponse, *XoloUsageTracker) {
	t.Helper()

	orgID := model.OrgID("org-1")
	providerID := model.NewProviderID()
	provider := model.NewProvider(orgID, "openai", "openai", "https://api.openai.com", "key", "USD")
	llmModel := model.NewLLMModel(providerID, orgID, "cadoles/test-model", "real/model", "desc", 1000, 2000)
	org := model.NewOrganization(testTenantID, "org-1", "Org 1", "", "USD")

	tracker, usageStore := newTestTracker(&fakeProviderStore{provider: provider, llmModel: llmModel}, &fakeOrgStore{org: org})
	req, res := newTestRequestResponse(llmModel.ID(), orgID, tokens)
	res.Interruption = interruption
	return usageStore, req, res, tracker
}

// TestUsageTrackerChargesReportedPartialUsage covers the provider that does
// publish usage as the stream runs, Anthropic and friends: the counts are real
// and are charged as such.
func TestUsageTrackerChargesReportedPartialUsage(t *testing.T) {
	usageStore, req, res, tracker := interruptedFixture(t,
		&genaiProxy.TokenUsage{PromptTokens: 1000, CompletionTokens: 300, TotalTokens: 1300},
		&genaiProxy.StreamInterruption{
			Cause:         genaiProxy.StreamInterruptionUpstream,
			Err:           errors.New("upstream hung up"),
			ChunksEmitted: 7,
			PartialUsage:  true,
		})

	if _, err := tracker.PostResponse(context.Background(), req, res); err != nil {
		t.Fatalf("PostResponse: %v", err)
	}

	if usageStore.recorded == nil {
		t.Fatal("no usage recorded for an interrupted stream")
	}
	if got, want := usageStore.recorded.Status(), model.UsageStatusInterrupted; got != want {
		t.Errorf("Status() = %q, want %q", got, want)
	}
	if got, want := usageStore.recorded.CostSource(), model.CostSourceComputed; got != want {
		t.Errorf("CostSource() = %q, want %q: the provider reported these counts", got, want)
	}
	// 1000 prompt tokens at 1000 µ¢/1k + 300 completion tokens at 2000 µ¢/1k.
	if got, want := usageStore.recorded.Cost(), int64(1000+600); got != want {
		t.Errorf("Cost() = %d, want %d: partial usage must be charged", got, want)
	}
}

// TestUsageTrackerEstimatesUnreportedUsage is the case genai warns about and
// the one that decides whether #43 actually closes: most providers only report
// usage in the final chunk, which never arrives on an interrupted stream, so
// TokensUsed comes back entirely zero. Recording that as a free request would
// hand the customer the tokens the provider billed.
func TestUsageTrackerEstimatesUnreportedUsage(t *testing.T) {
	usageStore, req, res, tracker := interruptedFixture(t,
		&genaiProxy.TokenUsage{},
		&genaiProxy.StreamInterruption{
			Cause:         genaiProxy.StreamInterruptionUpstream,
			Err:           errors.New("upstream hung up"),
			ChunksEmitted: 40,
			PartialUsage:  false,
		})
	req.Body = []byte(`{"model":"cadoles/test-model","messages":[{"role":"user","content":"` + strings.Repeat("x", 400) + `"}]}`)

	if _, err := tracker.PostResponse(context.Background(), req, res); err != nil {
		t.Fatalf("PostResponse: %v", err)
	}

	if usageStore.recorded == nil {
		t.Fatal("no usage recorded for an interrupted stream")
	}
	if got, want := usageStore.recorded.CostSource(), model.CostSourceEstimated; got != want {
		t.Errorf("CostSource() = %q, want %q: the counts are estimates, not provider figures", got, want)
	}
	// 400 characters of prompt at 4 characters per token, 40 chunks of answer.
	if got, want := usageStore.recorded.PromptTokens(), 100; got != want {
		t.Errorf("PromptTokens() = %d, want %d", got, want)
	}
	if got, want := usageStore.recorded.CompletionTokens(), 40; got != want {
		t.Errorf("CompletionTokens() = %d, want %d", got, want)
	}
	if usageStore.recorded.Cost() == 0 {
		t.Error("Cost() = 0: an answer the client received must not be recorded as free")
	}
}

// TestUsageTrackerMapsEveryInterruptionCause walks the four causes genai
// reports. A cause left unmapped used to be recorded as a completed call,
// which no report could tell apart from a healthy request.
func TestUsageTrackerMapsEveryInterruptionCause(t *testing.T) {
	cases := []struct {
		cause genaiProxy.StreamInterruptionCause
		want  model.UsageStatus
	}{
		{genaiProxy.StreamInterruptionUpstream, model.UsageStatusInterrupted},
		{genaiProxy.StreamInterruptionClientGone, model.UsageStatusClientGone},
		{genaiProxy.StreamInterruptionWriteFailed, model.UsageStatusWriteFailed},
		{genaiProxy.StreamInterruptionTruncated, model.UsageStatusTruncated},
		// A cause this build does not know must not pass for a completed call.
		{genaiProxy.StreamInterruptionCause("something_new"), model.UsageStatusInterrupted},
	}

	for _, tc := range cases {
		t.Run(string(tc.cause), func(t *testing.T) {
			usageStore, req, res, tracker := interruptedFixture(t,
				&genaiProxy.TokenUsage{PromptTokens: 10, CompletionTokens: 5},
				&genaiProxy.StreamInterruption{Cause: tc.cause, PartialUsage: true})

			if _, err := tracker.PostResponse(context.Background(), req, res); err != nil {
				t.Fatalf("PostResponse: %v", err)
			}
			if got := usageStore.recorded.Status(); got != tc.want {
				t.Errorf("Status() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestUsageTrackerMarksCompletedStream guards the normal path: no interruption
// means a plain "ok" record with the provider's own counts.
func TestUsageTrackerMarksCompletedStream(t *testing.T) {
	usageStore, req, res, tracker := interruptedFixture(t,
		&genaiProxy.TokenUsage{PromptTokens: 10, CompletionTokens: 5}, nil)

	if _, err := tracker.PostResponse(context.Background(), req, res); err != nil {
		t.Fatalf("PostResponse: %v", err)
	}
	if got, want := usageStore.recorded.Status(), model.UsageStatusOK; got != want {
		t.Errorf("Status() = %q, want %q", got, want)
	}
	if got, want := usageStore.recorded.CostSource(), model.CostSourceComputed; got != want {
		t.Errorf("CostSource() = %q, want %q", got, want)
	}
}

func TestEstimatePromptTokens(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"string content", `{"messages":[{"role":"user","content":"` + strings.Repeat("a", 40) + `"}]}`, 10},
		{"multipart content", `{"messages":[{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("b", 80) + `"}]}]}`, 20},
		{"several messages", `{"messages":[{"content":"` + strings.Repeat("c", 20) + `"},{"content":"` + strings.Repeat("d", 20) + `"}]}`, 10},
		{"empty body", ``, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := estimatePromptTokens([]byte(tc.body)); got != tc.want {
				t.Errorf("estimatePromptTokens = %d, want %d", got, tc.want)
			}
		})
	}

	// A body that is not an OpenAI chat request still yields something rather
	// than claiming an empty prompt.
	if got := estimatePromptTokens([]byte(strings.Repeat("z", 40))); got != 10 {
		t.Errorf("unparseable body: got %d, want 10", got)
	}
}
