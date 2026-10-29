package api

import (
	"net/http"
	"strings"

	"github.com/xolo-gateway/xolo/internal/catalog"
)

// CatalogPrefill is what the model form can pre-fill from the open model
// catalog. Prices are per million tokens in Currency. Estimated lists the
// fields whose catalog value is an estimate rather than a published figure.
type CatalogPrefill struct {
	Name             string   `json:"name,omitempty"`
	Currency         string   `json:"currency,omitempty"`
	PromptCost       *float64 `json:"prompt_cost,omitempty"`
	CachedPromptCost *float64 `json:"cached_prompt_cost,omitempty"`
	CompletionCost   *float64 `json:"completion_cost,omitempty"`
	// PricesVary reports that the provider publishes other prices than the
	// base tier the form is filled with (by prompt size or time of day).
	PricesVary       bool     `json:"prices_vary,omitempty"`
	ContextWindow    int64    `json:"context_window,omitempty"`
	OutputWindow     int64    `json:"output_window,omitempty"`
	ActiveParams     float64  `json:"active_params,omitempty"` // billions, as the form takes them
	TokensPerSecLow  float64  `json:"tokens_per_sec_low,omitempty"`
	TokensPerSecHigh float64  `json:"tokens_per_sec_high,omitempty"`
	CapTools         *bool    `json:"cap_tools,omitempty"`
	CapVision        *bool    `json:"cap_vision,omitempty"`
	CapReasoning     *bool    `json:"cap_reasoning,omitempty"`
	CapAudio         *bool    `json:"cap_audio,omitempty"`
	APIType          string   `json:"api_type,omitempty"`
	APIBaseURL       string   `json:"api_base_url,omitempty"`
	Estimated        []string `json:"estimated,omitempty"`
	Version          string   `json:"version"`
}

// WithCatalog serves the model form's pre-fill from the open model catalog.
func (h *Handler) WithCatalog(source *catalog.Source) *Handler {
	h.catalog = source
	return h
}

// handleCatalogLookup answers what the catalog knows about the model id,
// served by a provider of the given type and base URL.
func (h *Handler) handleCatalogLookup(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id parameter")
		return
	}
	if h.catalog == nil {
		writeError(w, http.StatusNotFound, "no model catalog")
		return
	}
	providerType := strings.TrimSpace(r.URL.Query().Get("provider"))
	baseURL := strings.TrimSpace(r.URL.Query().Get("base_url"))

	c := h.catalog.Catalog(r.Context())
	found, ok := c.Lookup(id, baseURL, providerType)
	if !ok {
		writeError(w, http.StatusNotFound, "model not found in the catalog")
		return
	}
	writeJSON(w, http.StatusOK, catalogPrefill(c, found, providerType, baseURL))
}

func catalogPrefill(c *catalog.Catalog, found catalog.Prefill, providerType, baseURL string) CatalogPrefill {
	out := CatalogPrefill{Version: c.Version}
	estimated := func(field string, p catalog.Provenance) {
		if p.Status == catalog.StatusEstimated {
			out.Estimated = append(out.Estimated, field)
		}
	}
	if m := found.Model; m != nil {
		out.Name = m.Name
		if m.ContextWindow != nil {
			out.ContextWindow = m.ContextWindow.Value
			estimated("context_window", m.ContextWindow.Provenance)
		}
		if m.OutputWindow != nil {
			out.OutputWindow = m.OutputWindow.Value
			estimated("output_window", m.OutputWindow.Provenance)
		}
		if caps := m.Capabilities; caps != nil {
			out.CapTools, out.CapVision, out.CapReasoning, out.CapAudio = &caps.Tools, &caps.Vision, &caps.Reasoning, &caps.Audio
		}
		if m.Params != nil && m.Params.Active != nil {
			out.ActiveParams = float64(m.Params.Active.Value) / 1e9
			estimated("active_params", m.Params.Active.Provenance)
		}
	}
	if o := found.Offering; o != nil {
		out.Currency = found.Currency
		input, output := o.Prices.Input, o.Prices.Output
		out.PromptCost, out.CompletionCost = &input, &output
		if o.Prices.CachedInput > 0 {
			cached := o.Prices.CachedInput
			out.CachedPromptCost = &cached
		}
		out.PricesVary = o.Prices.Varies()
		estimated("prompt_cost", o.Prices.Provenance)
		if o.ContextWindow != nil {
			out.ContextWindow = o.ContextWindow.Value
		}
		if o.OutputWindow != nil {
			out.OutputWindow = o.OutputWindow.Value
		}
		if o.Throughput != nil {
			out.TokensPerSecLow, out.TokensPerSecHigh = o.Throughput.Low, o.Throughput.High
		}
		// The API is only pre-filled when it is not the provider's own.
		if o.APIType != "" && o.APIType != providerType {
			out.APIType = o.APIType
		}
		if o.BaseURL != "" && strings.TrimRight(o.BaseURL, "/") != strings.TrimRight(baseURL, "/") && out.APIType != "" {
			out.APIBaseURL = o.BaseURL
		}
	}
	return out
}
