package gorm_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	gormpkg "gorm.io/gorm"
)

func TestRecoveryPreservesPermanentMappingNames(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		seedLegacyRecovery(t, db)
		require.NoError(t, db.Create(&adapter.Alert{
			ID: "alert", OrgID: "org-acme", OwnerID: "user-alice", Query: `{user="user-alice"}`,
		}).Error)
		pool, err := db.DB()
		require.NoError(t, err)
		// Keep the search path and temporary-table checks on the migration connection.
		pool.SetMaxOpenConns(1)
		if db.Dialector.Name() == "postgres" {
			require.NoError(t, db.Exec("SELECT set_config('search_path', current_schema() || ', pg_temp', false)").Error)
		}
		for _, table := range []string{"uuid_recovery_ids", "uuid_recovery_changes"} {
			require.NoError(t, db.Exec("CREATE TABLE "+table+" (marker text NOT NULL)").Error)
			require.NoError(t, db.Exec("INSERT INTO "+table+" (marker) VALUES (?)", "keep").Error)
		}
		plan, err := adapter.PlanCommonRecovery(t.Context(), db)
		require.NoError(t, err)
		require.NoError(t, adapter.MigrateDatabase(t.Context(), db, plan))

		for _, table := range []string{"uuid_recovery_ids", "uuid_recovery_changes"} {
			markers := []string{}
			require.NoError(t, db.Table(table).Pluck("marker", &markers).Error)
			require.Equal(t, []string{"keep"}, markers)
		}
		var remaining int64
		query := "SELECT COUNT(*) FROM sqlite_temp_master WHERE name IN (?, ?)"
		if db.Dialector.Name() == "postgres" {
			query = "SELECT COUNT(*) FROM pg_class WHERE relnamespace = pg_my_temp_schema() AND relname IN (?, ?)"
		}
		require.NoError(t, db.Raw(query, "uuid_recovery_ids", "uuid_recovery_changes").Scan(&remaining).Error)
		require.Zero(t, remaining, "migration must drop its temporary tables")
		var alert adapter.Alert
		require.NoError(t, db.First(&alert, "id = ?", "alert").Error)
		require.Equal(t, plan.IDs["users"]["user-alice"], alert.OwnerID)
		require.Equal(t, plan.IDs["organizations"]["org-acme"], alert.OrgID)
		require.Equal(t, `{user="`+plan.IDs["users"]["user-alice"]+`"}`, alert.Query)
	})
}
