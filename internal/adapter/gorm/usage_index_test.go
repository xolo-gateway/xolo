package gorm_test

import (
	"context"
	"strings"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// The indexes the proxy hot path depends on. They are declared as struct tags,
// so they only reach a database through a migration: an index that exists in
// the code and nowhere in production would leave the per-request aggregations
// scanning the whole window.
var hotPathUsageIndexes = []string{
	"idx_usage_org_prov_plan", // subscription plan aggregations + active-user count
	"idx_usage_org_prov_user", // per-user presence probe
	"idx_usage_org_payg_cost", // PAYG cost sums
}

func TestUsageRecord_PlanIndexesExist(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newStoreOn(t, db)
		migrateSchema(t, store)

		for _, name := range hotPathUsageIndexes {
			if !db.Migrator().HasIndex("usage_records", name) {
				t.Errorf("index %s is missing on a fresh schema", name)
			}
		}
	})
}

func TestUsageRecord_PlanIndexIsAddedToAnExistingSchema(t *testing.T) {
	// The path the migration exists for: a database already migrated, where the
	// index must be added on top of an existing table. A fresh schema does not
	// exercise it, since InitSchema creates every index in one go.
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newStoreOn(t, db)
		migrateSchema(t, store)

		// Put the table back in the state a deployment predating the indexes is
		// in. Raw SQL rather than Migrator().DropIndex: on PostgreSQL the latter
		// qualifies the index with the current schema in a form the server
		// rejects under a search_path DSN, which is how the test backend connects.
		added := []string{"idx_usage_org_prov_plan", "idx_usage_org_prov_user"}
		for _, name := range added {
			if err := db.Exec("DROP INDEX " + db.Statement.Quote(name)).Error; err != nil {
				t.Fatalf("drop index %s: %v", name, err)
			}
			if db.Migrator().HasIndex("usage_records", name) {
				t.Fatalf("%s still present after drop; the test cannot exercise the migration", name)
			}
		}

		// This is exactly what migrations 202609150001 and 202609150002 run.
		if err := db.AutoMigrate(&xologorm.UsageRecord{}); err != nil {
			t.Fatalf("AutoMigrate: %v", err)
		}
		for _, name := range added {
			if !db.Migrator().HasIndex("usage_records", name) {
				t.Errorf("AutoMigrate did not add %s to an existing table", name)
			}
		}
	})
}

func TestUsageRecord_UserIndexIsAddedToAnExistingSchema(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newStoreOn(t, db)
		migrateSchema(t, store)

		// Raw SQL rather than Migrator().DropIndex, see above.
		if err := db.Exec("DROP INDEX " + db.Statement.Quote("idx_usage_user_created")).Error; err != nil {
			t.Fatalf("drop index: %v", err)
		}

		// This is what migration 202610300002 runs.
		if err := db.AutoMigrate(&xologorm.UsageRecord{}); err != nil {
			t.Fatalf("AutoMigrate: %v", err)
		}
		if !db.Migrator().HasIndex("usage_records", "idx_usage_user_created") {
			t.Error("AutoMigrate did not add idx_usage_user_created to an existing table")
		}
	})
}

// The personal pages filter on the user and a period, without an organization.
// On idx_usage_records_user_id SQLite read every record of the user, and sorted
// them all to return one page of the usage table.
func TestUsageRecord_PersonalQueriesUseTheUserIndex(t *testing.T) {
	db := openSQLite(t, newSQLiteDSN(t))
	migrateSchema(t, newStoreOn(t, db))

	// The shape applyUsageFilter gives them.
	userID, since := string(model.NewUserID()), time.Now().Add(-7*24*time.Hour)
	queries := map[string]struct {
		sql  string
		args []any
	}{
		"usage table page": {
			"SELECT * FROM usage_records WHERE user_id IN (?) AND created_at >= ? ORDER BY created_at DESC LIMIT 51",
			[]any{userID, since},
		},
		"period aggregate": {
			"SELECT COUNT(*), COALESCE(SUM(cost),0) FROM usage_records WHERE user_id IN (?) AND created_at >= ?",
			[]any{userID, since},
		},
		"virtual model aggregate": {
			"SELECT COUNT(*), COALESCE(SUM(cost),0) FROM usage_records WHERE user_id IN (?) AND proxy_model_name = ? AND created_at >= ?",
			[]any{userID, "acme/fast", since},
		},
	}
	for name, q := range queries {
		t.Run(name, func(t *testing.T) {
			var rows []struct{ Detail string }
			if err := db.Raw("EXPLAIN QUERY PLAN "+q.sql, q.args...).Scan(&rows).Error; err != nil {
				t.Fatalf("explain: %v", err)
			}
			var plan []string
			for _, r := range rows {
				plan = append(plan, r.Detail)
			}
			got := strings.Join(plan, " | ")
			if !strings.Contains(got, "idx_usage_user_created (user_id=? AND created_at>?)") {
				t.Errorf("plan does not bound the period on idx_usage_user_created: %s", got)
			}
			if strings.Contains(got, "TEMP B-TREE") {
				t.Errorf("plan sorts in a temp B-tree: %s", got)
			}
		})
	}
}

// migrateSchema runs the store's migrations by performing one write.
func migrateSchema(t *testing.T, store *xologorm.Store) {
	t.Helper()
	rec := model.NewUsageRecord(model.NewUserID(), "", model.NewOrgID(), "", model.NewProviderID(), model.NewLLMModelID(),
		"fast", "", 100, 10, 50, 0, "USD", model.CostSourceComputed, "")
	if err := store.RecordUsage(context.Background(), rec); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
}
