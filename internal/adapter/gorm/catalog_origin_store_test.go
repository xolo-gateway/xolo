package gorm_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/pkg/errors"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

func TestCatalogOriginStore(t *testing.T) {
	eachBackend(t, scenarioCatalogOriginStore)
}

func scenarioCatalogOriginStore(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	provider := model.NewProvider(org.ID(), "Mistral", "mistral", "https://api.mistral.ai/v1", "encrypted-key", "EUR")
	if err := store.CreateProvider(ctx, provider); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	tracked := model.NewLLMModel(provider.ID(), org.ID(), "mistral-large", "mistral-large-latest", "", 2000, 6000)
	untracked := model.NewLLMModel(provider.ID(), org.ID(), "local", "local", "", 0, 0)
	for _, m := range []model.LLMModel{tracked, untracked} {
		if err := store.CreateLLMModel(ctx, m); err != nil {
			t.Fatalf("CreateLLMModel: %v", err)
		}
	}

	if origin, err := store.GetCatalogOrigin(ctx, tracked.ID()); err != nil || origin != nil {
		t.Fatalf("a new model has no origin, got %+v, %v", origin, err)
	}

	origin := &model.CatalogOrigin{
		Provider: "mistral", Model: "mistral-large-latest", Version: "v0.1.0", Currency: "EUR",
		Catalog:   model.ProviderBundleModel{RealModel: "mistral-large-latest", Prices: model.ModelPrices{Input: 2, Output: 6}},
		Applied:   model.ProviderBundleModel{RealModel: "mistral-large-latest", Prices: model.ModelPrices{Input: 2, Output: 6}},
		Estimated: []string{"active_params"},
	}
	if err := store.SaveCatalogOrigin(ctx, tracked.ID(), origin); err != nil {
		t.Fatalf("SaveCatalogOrigin: %v", err)
	}
	got, err := store.GetCatalogOrigin(ctx, tracked.ID())
	if err != nil {
		t.Fatalf("GetCatalogOrigin: %v", err)
	}
	if !reflect.DeepEqual(got, origin) {
		t.Fatalf("origin = %+v, want %+v", got, origin)
	}

	// Saving the model, as the model form and the bundle import do, keeps
	// the origin: it is not part of the configuration they write.
	edited := model.NewLLMModelFromBundle(tracked.ID(), provider.ID(), org.ID(), tracked.CreatedAt(), model.ProviderBundleModel{
		RealModel: "mistral-large-latest", ProxyName: "mistral-large", Enabled: true, Prices: model.ModelPrices{Input: 3, Output: 6},
	})
	if err := store.SaveLLMModel(ctx, edited); err != nil {
		t.Fatalf("SaveLLMModel: %v", err)
	}
	if got, err := store.GetCatalogOrigin(ctx, tracked.ID()); err != nil || !reflect.DeepEqual(got, origin) {
		t.Fatalf("saving the model lost its origin: %+v, %v", got, err)
	}
	if m, err := store.GetLLMModelByID(ctx, tracked.ID()); err != nil || m.PromptCostPer1KTokens() != 3000 {
		t.Fatalf("the model was not saved: %v, %v", m, err)
	}

	list, err := store.ListCatalogTrackedModels(ctx)
	if err != nil {
		t.Fatalf("ListCatalogTrackedModels: %v", err)
	}
	if len(list) != 1 || list[0].ModelID != tracked.ID() || list[0].OrgID != org.ID() || list[0].ProviderID != provider.ID() || list[0].Origin.Version != "v0.1.0" {
		t.Fatalf("tracked models = %+v, want the tracked one only", list)
	}

	if err := store.SaveCatalogOrigin(ctx, tracked.ID(), nil); err != nil {
		t.Fatalf("SaveCatalogOrigin(nil): %v", err)
	}
	if got, err := store.GetCatalogOrigin(ctx, tracked.ID()); err != nil || got != nil {
		t.Fatalf("a forgotten origin reads %+v, %v", got, err)
	}
	if list, err := store.ListCatalogTrackedModels(ctx); err != nil || len(list) != 0 {
		t.Fatalf("a forgotten origin is still listed: %+v, %v", list, err)
	}

	if err := store.SaveCatalogOrigin(ctx, model.NewLLMModelID(), origin); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("SaveCatalogOrigin on a missing model: %v, want ErrNotFound", err)
	}
	if _, err := store.GetCatalogOrigin(ctx, model.NewLLMModelID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetCatalogOrigin on a missing model: %v, want ErrNotFound", err)
	}
}
