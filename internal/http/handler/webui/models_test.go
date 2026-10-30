package webui

import (
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/profile/component"
)

// stubLLMModel lets tests build an LLMModel with only the bits the catalog
// filters and sorts on, without standing up the full model.
type stubLLMModel struct {
	name        string
	description string
	prompt      int64
	completion  int64
	virtual     bool
	kind        model.ModelKind
	context     int64
	caps        model.ModelCapabilities
	created     time.Time
}

func (s *stubLLMModel) ID() model.LLMModelID               { return model.LLMModelID(s.name) }
func (s *stubLLMModel) PromptCostPer1KTokens() int64       { return s.prompt }
func (s *stubLLMModel) CachedPromptCostPer1KTokens() int64 { return s.prompt }
func (s *stubLLMModel) CompletionCostPer1KTokens() int64   { return s.completion }
func (s *stubLLMModel) IsVirtual() bool                    { return s.virtual }
func (s *stubLLMModel) ProviderID() model.ProviderID       { return "" }
func (s *stubLLMModel) OrgID() model.OrgID                 { return "" }
func (s *stubLLMModel) ProxyName() string                  { return s.name }
func (s *stubLLMModel) RealModel() string                  { return "" }
func (s *stubLLMModel) Description() string                { return s.description }
func (s *stubLLMModel) Kind() model.ModelKind {
	if s.kind == "" {
		return model.ModelKindCompletion
	}
	return s.kind
}
func (s *stubLLMModel) Enabled() bool                             { return true }
func (s *stubLLMModel) ContextWindow() int64                      { return s.context }
func (s *stubLLMModel) OutputWindow() int64                       { return 0 }
func (s *stubLLMModel) ActiveParams() int64                       { return 0 }
func (s *stubLLMModel) TokensPerSecLow() float64                  { return 0 }
func (s *stubLLMModel) TokensPerSecHigh() float64                 { return 0 }
func (s *stubLLMModel) Capabilities() model.ModelCapabilities     { return s.caps }
func (s *stubLLMModel) CreatedAt() time.Time                      { return s.created }
func (s *stubLLMModel) UpdatedAt() time.Time                      { return s.created }
func (s *stubLLMModel) TokenLimitConfig() *model.TokenLimitConfig { return nil }
func (s *stubLLMModel) ExtraBody() map[string]any                 { return nil }
func (s *stubLLMModel) APIType() string                           { return "" }
func (s *stubLLMModel) BaseURL() string                           { return "" }

var _ model.LLMModel = (*stubLLMModel)(nil)

func catalogModel(m *stubLLMModel, provider string) component.CatalogModel {
	cm := component.CatalogModel{Model: m, Names: []string{"acme/" + m.name}}
	if provider != "" {
		cm.Provider = model.NewProvider("", provider, "openai", "", "", "EUR")
	}
	return cm
}

func catalogNames(models []component.CatalogModel) []string {
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.Model.ProxyName())
	}
	return names
}

func testCatalog() []component.CatalogModel {
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return []component.CatalogModel{
		catalogModel(&stubLLMModel{name: "mistral-large", description: "Flagship model", prompt: 2, completion: 6, context: 128_000,
			caps: model.ModelCapabilities{Tools: true}, created: day}, "Mistral"),
		catalogModel(&stubLLMModel{name: "deepseek-flash", description: "Fast and cheap", prompt: 1, completion: 1, context: 1_000_000,
			caps: model.ModelCapabilities{Tools: true, Reasoning: true}, created: day.AddDate(0, 0, 2)}, "OpenRouter"),
		catalogModel(&stubLLMModel{name: "pixtral", prompt: 3, completion: 3,
			caps: model.ModelCapabilities{Vision: true}, created: day.AddDate(0, 0, 1)}, "Mistral"),
		catalogModel(&stubLLMModel{name: "embed", prompt: 1, kind: model.ModelKindEmbeddings, context: 8_000, created: day}, "Mistral"),
		catalogModel(&stubLLMModel{name: "coder", description: "Routes to deepseek", virtual: true, context: 64_000,
			caps: model.ModelCapabilities{Tools: true}, created: day.AddDate(0, 0, 3)}, ""),
	}
}

// TestParseModelsFilter pins how the query string becomes a filter: unknown
// values are dropped so the controls never show a choice the list ignores.
func TestParseModelsFilter(t *testing.T) {
	filter := parseModelsFilter(url.Values{
		"q":        {"  deep  "},
		"provider": {"Mistral"},
		"kind":     {"bogus"},
		"type":     {"virtual"},
		"sort":     {"usage"},
		"cap":      {"tools", "telepathy", "tools", "vision"},
	})
	want := component.ModelsFilter{
		Query:        "deep",
		Provider:     "Mistral",
		Type:         component.ModelsTypeVirtual,
		Sort:         component.ModelsSortName,
		Capabilities: []string{"tools", "vision"},
	}
	if filter.Query != want.Query || filter.Provider != want.Provider || filter.Kind != "" ||
		filter.Type != want.Type || filter.Sort != want.Sort || !slices.Equal(filter.Capabilities, want.Capabilities) {
		t.Errorf("got %+v, want %+v", filter, want)
	}
	if parseModelsFilter(url.Values{}).Active() {
		t.Errorf("an empty query string must not narrow the list")
	}
}

func TestFilterCatalogModels(t *testing.T) {
	cases := []struct {
		name   string
		filter component.ModelsFilter
		want   []string
	}{
		{
			name: "no filter lists every model by name",
			want: []string{"coder", "deepseek-flash", "embed", "mistral-large", "pixtral"},
		},
		{
			name:   "search matches the description and every term must match",
			filter: component.ModelsFilter{Query: "DeepSeek routes"},
			want:   []string{"coder"},
		},
		{
			name:   "search matches the provider name",
			filter: component.ModelsFilter{Query: "openrouter"},
			want:   []string{"deepseek-flash"},
		},
		{
			name:   "provider excludes virtual models",
			filter: component.ModelsFilter{Provider: "Mistral"},
			want:   []string{"embed", "mistral-large", "pixtral"},
		},
		{
			name:   "capabilities must all be present",
			filter: component.ModelsFilter{Capabilities: []string{"tools", "reasoning"}},
			want:   []string{"deepseek-flash"},
		},
		{
			name:   "kind",
			filter: component.ModelsFilter{Kind: string(model.ModelKindEmbeddings)},
			want:   []string{"embed"},
		},
		{
			name:   "direct models only",
			filter: component.ModelsFilter{Type: component.ModelsTypeDirect, Capabilities: []string{"tools"}},
			want:   []string{"deepseek-flash", "mistral-large"},
		},
		{
			name:   "virtual models only",
			filter: component.ModelsFilter{Type: component.ModelsTypeVirtual},
			want:   []string{"coder"},
		},
		{
			name:   "price puts the cheapest first and virtual models last",
			filter: component.ModelsFilter{Sort: component.ModelsSortPrice},
			want:   []string{"embed", "deepseek-flash", "pixtral", "mistral-large", "coder"},
		},
		{
			name:   "newest first, then by name",
			filter: component.ModelsFilter{Sort: component.ModelsSortNewest},
			want:   []string{"coder", "deepseek-flash", "pixtral", "embed", "mistral-large"},
		},
		{
			name:   "largest context first, unknown last",
			filter: component.ModelsFilter{Sort: component.ModelsSortContext},
			want:   []string{"deepseek-flash", "mistral-large", "coder", "embed", "pixtral"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := catalogNames(filterCatalogModels(testCatalog(), tc.filter)); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCatalogFacets pins which controls the filter bar offers: only those
// that can narrow the list.
func TestCatalogFacets(t *testing.T) {
	facets := catalogFacets(testCatalog())
	if !slices.Equal(facets.Providers, []string{"Mistral", "OpenRouter"}) {
		t.Errorf("providers: got %v", facets.Providers)
	}
	if !slices.Equal(facets.Kinds, []model.ModelKind{model.ModelKindCompletion, model.ModelKindEmbeddings}) {
		t.Errorf("kinds: got %v", facets.Kinds)
	}
	if !facets.Virtual {
		t.Errorf("a list mixing direct and virtual models must offer the type filter")
	}

	virtualOnly := catalogFacets(testCatalog()[4:])
	if virtualOnly.Virtual || len(virtualOnly.Providers) != 0 || len(virtualOnly.Kinds) != 1 {
		t.Errorf("a list of virtual models only has nothing to narrow, got %+v", virtualOnly)
	}
}
