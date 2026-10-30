package service

import (
	"context"
	"errors"
	"testing"

	"github.com/xolo-gateway/xolo/internal/catalog"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

func (s *memoryProviderStore) GetProviderByID(_ context.Context, id model.ProviderID) (model.Provider, error) {
	p, ok := s.providers[id]
	if !ok {
		return nil, port.ErrNotFound
	}
	return p, nil
}

// memoryOriginStore is a port.CatalogOriginStore over a memoryProviderStore.
type memoryOriginStore struct {
	models  *memoryProviderStore
	origins map[model.LLMModelID]model.CatalogOrigin
	saveErr error
}

func (s *memoryOriginStore) GetCatalogOrigin(_ context.Context, id model.LLMModelID) (*model.CatalogOrigin, error) {
	if _, ok := s.models.models[id]; !ok {
		return nil, port.ErrNotFound
	}
	o, ok := s.origins[id]
	if !ok {
		return nil, nil
	}
	return &o, nil
}

func (s *memoryOriginStore) ListCatalogTrackedModels(context.Context) ([]model.CatalogTrackedModel, error) {
	var tracked []model.CatalogTrackedModel
	for id, o := range s.origins {
		m := s.models.models[id]
		tracked = append(tracked, model.CatalogTrackedModel{ModelID: id, OrgID: m.OrgID(), ProviderID: m.ProviderID(), Origin: o})
	}
	return tracked, nil
}

func (s *memoryOriginStore) SaveCatalogOrigin(_ context.Context, id model.LLMModelID, o *model.CatalogOrigin) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	if o == nil {
		delete(s.origins, id)
		return nil
	}
	s.origins[id] = *o
	return nil
}

// fixedRate converts at rate, or fails when err is set.
type fixedRate struct {
	rate float64
	err  error
}

func (r *fixedRate) Convert(_ context.Context, amount int64, _, _ string) (int64, error) {
	if r.err != nil {
		return 0, r.err
	}
	return int64(float64(amount) * r.rate), nil
}

// followRelease is a release with one provider at https://api.example.com/v1
// serving one model, priced input/output per million tokens in USD.
func followRelease(version string, input, output float64, contextWindow int64) *catalog.Catalog {
	p := catalog.Provider{ID: "example", Name: "Example", Currency: "USD"}
	p.Xolo.Type, p.Xolo.BaseURL = "openai", "https://api.example.com/v1"
	p.Offerings = []catalog.Offering{{
		Model: "large", ModelID: "large-latest",
		Prices: catalog.Prices{Input: input, Output: output, Provenance: catalog.Provenance{Status: catalog.StatusPublished}},
	}}
	return &catalog.Catalog{
		Version: version,
		Models: []catalog.Model{{
			ID: "large", Name: "Large", Kind: "completion",
			ContextWindow: &catalog.Count{Value: contextWindow, Provenance: catalog.Provenance{Status: catalog.StatusEstimated}},
			Capabilities:  &catalog.Capabilities{Tools: true},
		}},
		Providers: []catalog.Provider{p},
	}
}

// importedFromCatalog is a provider in currency with the model of release
// c imported, its origin recorded as the catalog import records it.
func importedFromCatalog(t *testing.T, c *catalog.Catalog, currency string, rates CurrencyConverter) (*memoryProviderStore, *memoryOriginStore, model.Provider, model.LLMModelID) {
	t.Helper()
	ctx := context.Background()
	store := newMemoryProviderStore()
	origins := &memoryOriginStore{models: store, origins: map[model.LLMModelID]model.CatalogOrigin{}}
	p := model.NewProvider("org", "Example", "openai", "https://api.example.com/v1", "key", currency)
	if err := store.CreateProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	tb, err := c.BundleFor("example", "", p)
	if err != nil {
		t.Fatal(err)
	}
	converted := tb.Bundle
	converted.Models = append([]model.ProviderBundleModel(nil), tb.Bundle.Models...)
	if currency != converted.Provider.Currency {
		if err := ConvertBundleCurrency(ctx, rates, &converted, currency); err != nil {
			t.Fatal(err)
		}
	}
	m := model.NewLLMModelFromBundle(model.NewLLMModelID(), p.ID(), p.OrgID(), p.CreatedAt(), converted.Models[0])
	if err := store.CreateLLMModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	origins.origins[m.ID()] = NewCatalogImport("example", c.Version, tb).Origin(tb.Bundle.Models[0], converted.Models[0])
	return store, origins, p, m.ID()
}

func TestCatalogFollower_TakesWhatTheCatalogChangedAndKeepsWhatTheOperatorChanged(t *testing.T) {
	ctx := context.Background()
	store, origins, _, id := importedFromCatalog(t, followRelease("v1", 2, 6, 128_000), "USD", nil)

	// The operator raises the context window; the prices stay the catalog's.
	edited := model.BundleModelOf(store.models[id])
	edited.ContextWindow = 64_000
	store.models[id] = model.NewLLMModelFromBundle(id, store.models[id].ProviderID(), store.models[id].OrgID(), store.models[id].CreatedAt(), edited)

	follower := NewCatalogFollower(store, origins, nil)
	report, err := follower.Follow(ctx, followRelease("v2", 3, 9, 256_000))
	if err != nil {
		t.Fatal(err)
	}
	m := store.models[id]
	if m.PromptCostPer1KTokens() != 3000 || m.CompletionCostPer1KTokens() != 9000 {
		t.Fatalf("prices = %d / %d, want the new catalog prices", m.PromptCostPer1KTokens(), m.CompletionCostPer1KTokens())
	}
	if m.ContextWindow() != 64_000 {
		t.Fatalf("context window = %d, want the operator's 64000 kept", m.ContextWindow())
	}
	if report.Models != 1 || report.Updated != 1 || report.Fields != 1 || report.Kept != 1 || report.Failed != 0 {
		t.Fatalf("report = %+v", report)
	}
	origin := origins.origins[id]
	if origin.Version != "v2" || origin.Catalog.ContextWindow != 256_000 || origin.Applied.ContextWindow != 256_000 || origin.Applied.Prices.Input != 3 {
		t.Fatalf("origin = %+v, want the v2 values", origin)
	}
	// The window the operator kept is offered, with the catalog's value.
	divergences := CatalogDivergences(m, origin)
	if len(divergences) != 1 || divergences[0].Field != BundleFieldWindows || divergences[0].Catalog != "256000 / 0" {
		t.Fatalf("divergences = %+v, want the windows", divergences)
	}

	// Following the same release again writes nothing.
	writes := store.writes
	if report, err := follower.Follow(ctx, followRelease("v2", 3, 9, 256_000)); err != nil || report.Models != 0 || store.writes != writes {
		t.Fatalf("a second follow wrote: %+v, %v, %d writes", report, err, store.writes-writes)
	}
}

func TestCatalogFollower_ConvertsANewPriceAndLeavesAnUnchangedOneAtItsRate(t *testing.T) {
	ctx := context.Background()
	rates := &fixedRate{rate: 0.5}
	store, origins, _, id := importedFromCatalog(t, followRelease("v1", 2, 6, 128_000), "EUR", rates)
	if got := store.models[id].PromptCostPer1KTokens(); got != 1000 {
		t.Fatalf("imported prompt cost = %d, want 1000 (2 USD at 0.5)", got)
	}

	// The rate moves, the catalog prices do not: the model keeps the prices
	// it was imported with.
	rates.rate = 0.8
	follower := NewCatalogFollower(store, origins, rates)
	if _, err := follower.Follow(ctx, followRelease("v2", 2, 6, 128_000)); err != nil {
		t.Fatal(err)
	}
	if got := store.models[id].PromptCostPer1KTokens(); got != 1000 {
		t.Fatalf("prompt cost = %d after a rate change alone, want 1000 kept", got)
	}
	if origins.origins[id].Version != "v2" || origins.origins[id].Applied.Prices.Input != 1 {
		t.Fatalf("origin = %+v, want v2 with the applied prices kept", origins.origins[id])
	}

	// The catalog changes its prices: converted at today's rate.
	if _, err := follower.Follow(ctx, followRelease("v3", 4, 6, 128_000)); err != nil {
		t.Fatal(err)
	}
	if got := store.models[id].PromptCostPer1KTokens(); got != 3200 {
		t.Fatalf("prompt cost = %d, want 3200 (4 USD at 0.8)", got)
	}
}

func TestCatalogFollower_LeavesAProviderItCannotConvertForLater(t *testing.T) {
	ctx := context.Background()
	rates := &fixedRate{rate: 0.5}
	store, origins, _, id := importedFromCatalog(t, followRelease("v1", 2, 6, 128_000), "EUR", rates)
	rates.err = errors.New("no rate")
	follower := NewCatalogFollower(store, origins, rates)
	if _, err := follower.Follow(ctx, followRelease("v2", 4, 6, 128_000)); err != nil {
		t.Fatal(err)
	}
	if got := store.models[id].PromptCostPer1KTokens(); got != 1000 {
		t.Fatalf("prompt cost = %d without a rate, want 1000 kept", got)
	}
	if origins.origins[id].Version != "v1" {
		t.Fatalf("origin version = %q, want v1 kept to follow v2 later", origins.origins[id].Version)
	}
	rates.err = nil
	if _, err := follower.Follow(ctx, followRelease("v2", 4, 6, 128_000)); err != nil {
		t.Fatal(err)
	}
	if got := store.models[id].PromptCostPer1KTokens(); got != 2000 {
		t.Fatalf("prompt cost = %d once the rate is back, want 2000", got)
	}
}

func TestCatalogFollower_FollowsAModelTheStoreRefusedAgainNextTime(t *testing.T) {
	ctx := context.Background()
	store, origins, _, id := importedFromCatalog(t, followRelease("v1", 2, 6, 128_000), "USD", nil)
	store.refuse = map[string]bool{"large-latest": true}
	follower := NewCatalogFollower(store, origins, nil)
	report, err := follower.Follow(ctx, followRelease("v2", 3, 6, 128_000))
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 1 || origins.origins[id].Version != "v1" {
		t.Fatalf("report = %+v, origin %q: want the failure reported and v1 kept", report, origins.origins[id].Version)
	}
	store.refuse = nil
	if _, err := follower.Follow(ctx, followRelease("v2", 3, 6, 128_000)); err != nil {
		t.Fatal(err)
	}
	if got := store.models[id].PromptCostPer1KTokens(); got != 3000 {
		t.Fatalf("prompt cost = %d, want the v2 price once the store takes it", got)
	}
}

func TestCatalogFollower_KeepsAModelWhoseOfferingIsGone(t *testing.T) {
	ctx := context.Background()
	store, origins, _, id := importedFromCatalog(t, followRelease("v1", 2, 6, 128_000), "USD", nil)
	release := followRelease("v2", 3, 6, 128_000)
	release.Providers[0].Offerings = nil
	follower := NewCatalogFollower(store, origins, nil)
	if _, err := follower.Follow(ctx, release); err != nil {
		t.Fatal(err)
	}
	if got := store.models[id].PromptCostPer1KTokens(); got != 2000 || origins.origins[id].Version != "v1" {
		t.Fatalf("prompt cost %d, origin %q: want the model and its origin kept", got, origins.origins[id].Version)
	}
}

func TestCatalogFollower_KeepsAnAPIChangeTheModelKindRefuses(t *testing.T) {
	ctx := context.Background()
	store, origins, _, id := importedFromCatalog(t, followRelease("v1", 2, 6, 128_000), "USD", nil)
	// The release moves the offering to the endpoint of an API that serves
	// no completion.
	release := followRelease("v2", 2, 6, 128_000)
	release.Providers[0].Offerings[0].APIType, release.Providers[0].Offerings[0].BaseURL = "typesafe", "https://decide.example.com"
	report, err := NewCatalogFollower(store, origins, nil).Follow(ctx, release)
	if err != nil {
		t.Fatal(err)
	}
	if store.models[id].APIType() != "" {
		t.Fatalf("api type = %q, want the change refused", store.models[id].APIType())
	}
	if report.Kept != 1 || report.Updated != 0 {
		t.Fatalf("report = %+v, want the refused change kept", report)
	}
	divergences := CatalogDivergences(store.models[id], origins.origins[id])
	if len(divergences) != 1 || divergences[0].Field != BundleFieldAPI {
		t.Fatalf("divergences = %+v, want the API offered", divergences)
	}
	// Accepting it is refused the same way.
	p := store.providers[store.models[id].ProviderID()]
	if err := AcceptCatalogValue(ctx, store, p, store.models[id], origins.origins[id], BundleFieldAPI); !errors.Is(err, port.ErrInvalid) {
		t.Fatalf("AcceptCatalogValue = %v, want ErrInvalid", err)
	}
}

func TestAcceptCatalogValue_WritesTheCatalogValueOfOneField(t *testing.T) {
	ctx := context.Background()
	store, origins, p, id := importedFromCatalog(t, followRelease("v1", 2, 6, 128_000), "USD", nil)
	edited := model.BundleModelOf(store.models[id])
	edited.ContextWindow, edited.Prices.Input = 64_000, 5
	store.models[id] = model.NewLLMModelFromBundle(id, p.ID(), p.OrgID(), p.CreatedAt(), edited)
	if CatalogEstimated(store.models[id], origins.origins[id], "context_window") {
		t.Fatal("a window the operator changed is no catalog estimate")
	}

	if err := AcceptCatalogValue(ctx, store, p, store.models[id], origins.origins[id], BundleFieldWindows); err != nil {
		t.Fatal(err)
	}
	m := store.models[id]
	if m.ContextWindow() != 128_000 || m.PromptCostPer1KTokens() != 5000 {
		t.Fatalf("window %d, prompt cost %d: want the catalog window and the operator's price", m.ContextWindow(), m.PromptCostPer1KTokens())
	}
	if !CatalogEstimated(m, origins.origins[id], "context_window") || CatalogEstimated(m, origins.origins[id], "prompt_cost") {
		t.Fatal("want the window flagged as the catalog's estimate, the published price not")
	}
	if err := AcceptCatalogValue(ctx, store, p, m, origins.origins[id], BundleFieldProxyName); !errors.Is(err, port.ErrInvalid) {
		t.Fatalf("accepting a field the catalog does not give = %v, want ErrInvalid", err)
	}
}
