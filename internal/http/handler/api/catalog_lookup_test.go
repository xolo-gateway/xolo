package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/catalog"
)

// lookupCatalog is a fixed catalog: one provider serving a model through its
// own API at another endpoint, another through the Anthropic API, a third
// with estimated prices and a narrower window than the model's.
func lookupCatalog() *catalog.Catalog {
	published := catalog.Provenance{Status: catalog.StatusPublished, Source: "https://example.test", Date: "2026-10-29"}
	estimated := catalog.Provenance{Status: catalog.StatusEstimated, Source: "https://example.test", Date: "2026-10-29", Note: "inferred"}
	p := catalog.Provider{ID: "acme", Name: "Acme", Currency: "USD", Offerings: []catalog.Offering{
		{Model: "glm", ModelID: "glm", APIType: "openai", BaseURL: "https://acme.test/openai/v1", Prices: catalog.Prices{Input: 1, Output: 2, Provenance: published}},
		{Model: "embed", ModelID: "embed", Prices: catalog.Prices{Input: 0.1, Provenance: published}},
		{Model: "haiku", ModelID: "haiku", APIType: "anthropic", BaseURL: "https://acme.test/anthropic/v1", Prices: catalog.Prices{Input: 1, Output: 5, Provenance: published}},
		{Model: "glm", ModelID: "glm-cheap", Prices: catalog.Prices{Input: 0.1, CachedInput: 0.01, Output: 0.2, Provenance: estimated},
			ContextWindow: &catalog.Count{Value: 64_000, Provenance: published},
			Throughput:    &catalog.Throughput{Low: 40, High: 80, Provenance: estimated}},
	}}
	p.Xolo.Type, p.Xolo.BaseURL = "openai", "https://acme.test/v1"
	return &catalog.Catalog{Kind: catalog.Kind, Format: catalog.Format, Version: "test",
		Models: []catalog.Model{
			{ID: "glm", Name: "GLM", Vendor: "Z.ai", ContextWindow: &catalog.Count{Value: 200_000, Provenance: estimated},
				Params: &catalog.Params{Active: &catalog.Count{Value: 40_000_000_000, Provenance: estimated}}},
			{ID: "haiku", Name: "Haiku", Vendor: "Anthropic"},
			{ID: "embed", Name: "Embed", Vendor: "Acme", Kind: "embeddings"},
		},
		Providers: []catalog.Provider{p},
	}
}

func lookup(t *testing.T, query string) (int, CatalogPrefill) {
	t.Helper()
	h := &Handler{catalog: catalog.NewStaticSource(lookupCatalog())}
	rr := httptest.NewRecorder()
	h.handleCatalogLookup(rr, httptest.NewRequest(http.MethodGet, "/api/catalog/lookup?"+query, nil))
	var got CatalogPrefill
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	return rr.Code, got
}

func estimatedFields(p CatalogPrefill) map[string]bool {
	fields := map[string]bool{}
	for _, f := range p.Estimated {
		fields[f] = true
	}
	return fields
}

func TestCatalogLookup_FillsTheEndpointOfTheProvidersOwnAPI(t *testing.T) {
	code, got := lookup(t, "id=glm&provider=openai&base_url=https://acme.test/v1")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if got.APIType != "" || got.APIBaseURL != "https://acme.test/openai/v1" {
		t.Errorf("api = %q %q, want the provider's own type at the offering's endpoint", got.APIType, got.APIBaseURL)
	}
	if fields := estimatedFields(got); !fields["active_params"] || !fields["context_window"] || fields["prompt_cost"] {
		t.Errorf("estimated = %v, want the model's estimates only", got.Estimated)
	}
}

func TestCatalogLookup_FillsTheAPIOfAModelServedThroughAnother(t *testing.T) {
	_, got := lookup(t, "id=haiku&provider=openai&base_url=https://acme.test/v1")
	if got.APIType != "anthropic" || got.APIBaseURL != "https://acme.test/anthropic/v1" {
		t.Errorf("api = %q %q, want the Anthropic API the provider serves it through", got.APIType, got.APIBaseURL)
	}
}

func TestCatalogLookup_FlagsEveryEstimatedPriceAndTheOfferingsWindow(t *testing.T) {
	_, got := lookup(t, "id=glm-cheap&provider=openai&base_url=https://acme.test/v1")
	fields := estimatedFields(got)
	if !fields["prompt_cost"] || !fields["completion_cost"] || !fields["cached_prompt_cost"] {
		t.Errorf("estimated = %v, want every price", got.Estimated)
	}
	if got.TokensPerSecLow != 40 || !fields["tokens_per_sec_low"] || !fields["tokens_per_sec_high"] {
		t.Errorf("throughput = %v estimated=%v, want the estimated throughput flagged", got.TokensPerSecLow, got.Estimated)
	}
	if got.ContextWindow != 64_000 || fields["context_window"] {
		t.Errorf("window = %d estimated=%v, want the offering's published window", got.ContextWindow, fields["context_window"])
	}
}

func TestCatalogLookup_UnknownModel(t *testing.T) {
	if code, _ := lookup(t, "id=unknown"); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

func TestCatalogLookup_NeverFillsAnotherProvidersEndpoint(t *testing.T) {
	// A local provider the catalog does not list: the prices and windows of
	// the model help, the endpoint of Acme's offering must not be copied.
	_, got := lookup(t, "id=haiku&provider=openai&base_url=http://localhost:11434/v1")
	if got.APIType != "" || got.APIBaseURL != "" {
		t.Errorf("api = %q %q, want no endpoint from another provider", got.APIType, got.APIBaseURL)
	}
	if got.PromptCost == nil {
		t.Error("the prices should still be pre-filled")
	}
}

func TestCatalogLookup_DisabledOrUnavailable(t *testing.T) {
	rr := httptest.NewRecorder()
	(&Handler{}).handleCatalogLookup(rr, httptest.NewRequest(http.MethodGet, "/api/catalog/lookup?id=glm", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("disabled: status = %d, want 404", rr.Code)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	source := catalog.NewSource(down.URL, time.Hour)
	source.Refresh(context.Background())
	rr = httptest.NewRecorder()
	(&Handler{catalog: source}).handleCatalogLookup(rr, httptest.NewRequest(http.MethodGet, "/api/catalog/lookup?id=glm", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("unavailable: status = %d, want 503", rr.Code)
	}
}

func TestCatalogLookup_FillsTheModelKind(t *testing.T) {
	_, got := lookup(t, "id=embed&provider=openai&base_url=https://acme.test/v1")
	if got.Kind != "embeddings" {
		t.Errorf("kind = %q, want the embeddings kind of the catalog model", got.Kind)
	}
}

func TestCatalogLookup_PrefersTheProviderWhoseOfferingEndpointItIsAt(t *testing.T) {
	c := lookupCatalog()
	beta := catalog.Provider{ID: "beta", Name: "Beta", Currency: "USD", Offerings: []catalog.Offering{
		{Model: "haiku", ModelID: "haiku", Prices: catalog.Prices{Input: 9, Output: 9}},
	}}
	beta.Xolo.Type, beta.Xolo.BaseURL = "anthropic", "https://beta.test/v1"
	c.Providers = append(c.Providers, beta)
	h := &Handler{catalog: catalog.NewStaticSource(c)}

	// A provider configured on Acme's Anthropic endpoint: Acme's offering,
	// not Beta's, though Beta's own type is anthropic.
	rr := httptest.NewRecorder()
	h.handleCatalogLookup(rr, httptest.NewRequest(http.MethodGet, "/api/catalog/lookup?id=haiku&provider=anthropic&base_url=https://acme.test/anthropic/v1", nil))
	var got CatalogPrefill
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.PromptCost == nil || *got.PromptCost != 1 || got.APIType != "" || got.APIBaseURL != "" {
		t.Errorf("prefill = %+v, want Acme's prices with its endpoint already the provider's", got)
	}
}

func TestCatalogLookup_FillsTheAPIOfAnOfferingServedAtTheProvidersAddress(t *testing.T) {
	// Acme's own endpoint is elsewhere, its Anthropic offering is at the
	// provider's address: the model takes that API, the endpoint is
	// already the provider's.
	_, got := lookup(t, "id=haiku&provider=openai&base_url=https://acme.test/anthropic/v1")
	if got.APIType != "anthropic" || got.APIBaseURL != "" {
		t.Errorf("api = %q %q, want anthropic at the provider's own address", got.APIType, got.APIBaseURL)
	}
}
