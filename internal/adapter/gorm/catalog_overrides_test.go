package gorm_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
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

// TestUpgradeAddsCatalogOverridesColumn covers 202610150001 on an instance
// created before the overrides existed. A fresh install goes through
// InitSchema, which records the migration without running it.
func TestUpgradeAddsCatalogOverridesColumn(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := context.Background()
		require.NoError(t, xologorm.NewStore(db).Migrate(ctx))

		// Put the schema back as the previous binary left it.
		require.NoError(t, db.Migrator().DropColumn(&xologorm.VirtualModel{}, "overrides_json"))
		require.NoError(t, db.Migrator().DropColumn(&xologorm.PersonalVirtualModel{}, "overrides_json"))
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610150001").Error)

		store := xologorm.NewStore(db)
		require.NoError(t, store.Migrate(ctx))

		vm := model.NewVirtualModel(model.OrgID("upgrade-org"), "auto", "")
		vm.SetCatalogOverrides(&model.CatalogOverrides{ContextWindow: 8_000})
		require.NoError(t, store.CreateVirtualModel(ctx, vm))
		got, err := store.GetVirtualModelByID(ctx, vm.ID())
		require.NoError(t, err)
		require.Equal(t, int64(8_000), got.CatalogOverrides().ContextWindow)

		pvm := model.NewPersonalVirtualModel(model.UserID("upgrade-user"), "mine", "")
		pvm.SetCatalogOverrides(&model.CatalogOverrides{MaxOutputTokens: 512})
		require.NoError(t, store.CreatePersonalVirtualModel(ctx, pvm))
		gotPVM, err := store.GetPersonalVirtualModelByID(ctx, pvm.ID())
		require.NoError(t, err)
		require.Equal(t, int64(512), gotPVM.CatalogOverrides().MaxOutputTokens)
	})
}
