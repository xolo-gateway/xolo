package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xolo-gateway/xolo/internal/catalog"
)

func TestCatalogLookup_PrefillsFromTheSnapshot(t *testing.T) {
	h := &Handler{catalog: catalog.NewSource("", 0)}

	// GLM-5.3 at OpenCode Go: published prices, an estimated active
	// parameter count.
	req := httptest.NewRequest(http.MethodGet, "/api/catalog/lookup?id=glm-5.3&provider=openai&base_url=https://opencode.ai/inference/go/v1", nil)
	rr := httptest.NewRecorder()
	h.handleCatalogLookup(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var got CatalogPrefill
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PromptCost == nil || got.CompletionCost == nil || got.Currency != "USD" || got.ContextWindow == 0 {
		t.Errorf("prefill = %+v, want the offering's prices and the model's window", got)
	}
	if got.APIType != "" {
		t.Errorf("api type = %q, want none for a model served by the provider's own API", got.APIType)
	}
	estimated := map[string]bool{}
	for _, f := range got.Estimated {
		estimated[f] = true
	}
	if !estimated["active_params"] || estimated["prompt_cost"] {
		t.Errorf("estimated = %v, want the active parameters only", got.Estimated)
	}

	rr = httptest.NewRecorder()
	h.handleCatalogLookup(rr, httptest.NewRequest(http.MethodGet, "/api/catalog/lookup?id=unknown-model", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown model: status = %d, want 404", rr.Code)
	}
}

func TestCatalogLookup_FillsTheAPIOfAModelServedThroughAnother(t *testing.T) {
	h := &Handler{catalog: catalog.NewSource("", 0)}
	req := httptest.NewRequest(http.MethodGet, "/api/catalog/lookup?id=claude-haiku-5-5&provider=openai&base_url=https://opencode.ai/inference/go/v1", nil)
	rr := httptest.NewRecorder()
	h.handleCatalogLookup(rr, req)
	var got CatalogPrefill
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.APIType != "anthropic" || got.APIBaseURL == "" {
		t.Errorf("api = %q %q, want the Anthropic API OpenCode Go serves it through", got.APIType, got.APIBaseURL)
	}
}
