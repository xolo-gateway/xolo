package gorm_test

import (
	"context"
	"testing"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TestCatalogOverrides_RoundTrip checks that the catalogue overrides of org and
// personal virtual models survive a save, and that clearing them sticks.
func TestCatalogOverrides_RoundTrip(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		want := &model.CatalogOverrides{
			ContextWindow:   64_000,
			MaxOutputTokens: 8_192,
			Capabilities:    &model.ModelCapabilities{Tools: true, Vision: true},
		}

		vm := model.NewVirtualModel(model.OrgID("overrides-org"), "auto", "")
		vm.SetCatalogOverrides(want)
		if err := store.CreateVirtualModel(ctx, vm); err != nil {
			t.Fatalf("CreateVirtualModel: %v", err)
		}
		got, err := store.GetVirtualModelByID(ctx, vm.ID())
		if err != nil {
			t.Fatalf("GetVirtualModelByID: %v", err)
		}
		assertOverrides(t, got.CatalogOverrides(), want)

		vm.SetCatalogOverrides(nil)
		if err := store.SaveVirtualModel(ctx, vm); err != nil {
			t.Fatalf("SaveVirtualModel: %v", err)
		}
		got, err = store.GetVirtualModelByID(ctx, vm.ID())
		if err != nil {
			t.Fatalf("GetVirtualModelByID: %v", err)
		}
		if got.CatalogOverrides() != nil {
			t.Fatalf("cleared overrides came back as %+v", got.CatalogOverrides())
		}

		pvm := model.NewPersonalVirtualModel(model.UserID("overrides-user"), "mine", "")
		pvm.SetCatalogOverrides(want)
		if err := store.CreatePersonalVirtualModel(ctx, pvm); err != nil {
			t.Fatalf("CreatePersonalVirtualModel: %v", err)
		}
		gotPVM, err := store.GetPersonalVirtualModelByID(ctx, pvm.ID())
		if err != nil {
			t.Fatalf("GetPersonalVirtualModelByID: %v", err)
		}
		assertOverrides(t, gotPVM.CatalogOverrides(), want)
	})
}

func assertOverrides(t *testing.T, got, want *model.CatalogOverrides) {
	t.Helper()
	if got == nil {
		t.Fatal("overrides lost")
	}
	if got.ContextWindow != want.ContextWindow || got.MaxOutputTokens != want.MaxOutputTokens {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.Capabilities == nil || *got.Capabilities != *want.Capabilities {
		t.Errorf("capabilities = %+v, want %+v", got.Capabilities, want.Capabilities)
	}
}
