package gorm_test

import (
	"context"
	"testing"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// TestUsageRecord_PlanIndexesExist guards the indexes the hot path depends on.
// They are declared as struct tags, so they only reach a database through a
// migration: an index that exists in the code and nowhere in production would
// leave the per-request aggregations scanning the whole window.
func TestUsageRecord_PlanIndexesExist(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	store := newStoreOn(t, db)

	// Any write goes through the migrations, which is what creates the schema.
	rec := model.NewUsageRecord(model.NewUserID(), "", model.NewOrgID(), model.NewProviderID(), model.NewLLMModelID(),
		"fast", "", 100, 10, 50, 0, "USD", model.CostSourceComputed, "")
	if err := store.RecordUsage(context.Background(), rec); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	for _, name := range []string{
		"idx_usage_org_prov_plan", // subscription plan aggregations + active-user count
		"idx_usage_org_payg_cost", // PAYG cost sums
	} {
		if !db.Migrator().HasIndex("usage_records", name) {
			t.Errorf("index %s is missing: the aggregations it covers will scan the window", name)
		}
	}
}
