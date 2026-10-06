package gorm_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"gorm.io/gorm"
)

func TestMigrationSQLiteForeignKeyChecks(t *testing.T) {
	db := sqliteBackend().newDB(t)
	counter := &recoveryQueryCounter{Interface: db.Logger}
	measured := db.Session(&gorm.Session{Logger: counter})

	require.NoError(t, adapter.MigrateDatabase(t.Context(), measured, nil))
	require.EqualValues(t, 1, counter.foreignKeyChecks.Swap(0), "fresh schema must be checked")
	require.True(t, db.Migrator().HasTable("migration_lock"))

	require.NoError(t, adapter.MigrateDatabase(t.Context(), measured, nil))
	require.Zero(t, counter.foreignKeyChecks.Swap(0), "restart must not scan foreign keys")

	prepareInvitationUpgrade(t, db)
	require.NoError(t, adapter.MigrateDatabase(t.Context(), measured, nil))
	require.EqualValues(t, 1, counter.foreignKeyChecks.Swap(0), "non-UUID migrations must be checked")
	require.True(t, db.Migrator().HasIndex(&adapter.Membership{}, "idx_memberships_user_org"))

	require.NoError(t, adapter.MigrateDatabase(t.Context(), measured, nil))
	require.Zero(t, counter.foreignKeyChecks.Load(), "restart after an upgrade must not scan foreign keys")
}

func TestRecoverySQLiteForeignKeyChecks(t *testing.T) {
	db := sqliteBackend().newDB(t)
	seedLegacyRecovery(t, db)
	artifact, err := adapter.PlanCommonRecovery(t.Context(), db)
	require.NoError(t, err)
	counter := &recoveryQueryCounter{Interface: db.Logger}
	measured := db.Session(&gorm.Session{Logger: counter})

	require.NoError(t, adapter.MigrateDatabase(t.Context(), measured, artifact))
	require.EqualValues(t, 2, counter.foreignKeyChecks.Swap(0), "UUID conversion and the complete migration must both be checked")

	require.NoError(t, adapter.MigrateDatabase(t.Context(), measured, artifact))
	require.Zero(t, counter.foreignKeyChecks.Swap(0), "reapplying a completed plan must not scan foreign keys")

	require.NoError(t, adapter.MigrateDatabase(t.Context(), measured, nil))
	require.Zero(t, counter.foreignKeyChecks.Load(), "restart after recovery must not scan foreign keys")
}

func TestMigrationSQLiteForeignKeyViolationRollsBack(t *testing.T) {
	for _, enabled := range []int{0, 1} {
		t.Run(fmt.Sprintf("foreign_keys=%d", enabled), func(t *testing.T) {
			db := sqliteBackend().newDB(t)
			seedLegacyRecovery(t, db)
			prepareInvitationUpgrade(t, db)
			pool, err := db.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(1)
			require.NoError(t, db.Exec(fmt.Sprintf("PRAGMA foreign_keys = %d", enabled)).Error)
			artifact, err := adapter.PlanCommonRecovery(t.Context(), db)
			require.NoError(t, err)
			var markersBefore []string
			require.NoError(t, db.Table("migrations").Order("id").Pluck("id", &markersBefore).Error)

			// The UUID conversion has already checked its references when it writes
			// the checkpoint. Only the final check before commit can catch this.
			require.NoError(t, db.Callback().Create().After("gorm:create").Register("inject_foreign_key_violation", func(tx *gorm.DB) {
				if tx.Statement.Table != "common_recoveries" {
					return
				}
				probe := tx.Session(&gorm.Session{NewDB: true})
				if err := probe.Exec("CREATE TABLE migration_fk_probe (user_id text REFERENCES users(id))").Error; err != nil {
					tx.AddError(err)
					return
				}
				tx.AddError(probe.Exec("INSERT INTO migration_fk_probe (user_id) VALUES ('missing-user')").Error)
			}))
			counter := &recoveryQueryCounter{Interface: db.Logger}
			measured := db.Session(&gorm.Session{Logger: counter})
			err = adapter.MigrateDatabase(t.Context(), measured, artifact)
			require.NoError(t, db.Callback().Create().Remove("inject_foreign_key_violation"))
			require.ErrorContains(t, err, "migration foreign key check failed")
			require.EqualValues(t, 2, counter.foreignKeyChecks.Swap(0))

			var restored int
			require.NoError(t, db.Raw("PRAGMA foreign_keys").Scan(&restored).Error)
			require.Equal(t, enabled, restored)
			var markersAfter []string
			require.NoError(t, db.Table("migrations").Order("id").Pluck("id", &markersAfter).Error)
			require.Equal(t, markersBefore, markersAfter)
			require.False(t, db.Migrator().HasTable("migration_fk_probe"))
			require.False(t, db.Migrator().HasTable(&adapter.CommonRecovery{}))
			require.False(t, db.Migrator().HasIndex(&adapter.Membership{}, "idx_memberships_user_org"))
			var user adapter.User
			require.NoError(t, db.First(&user, "id = ?", "user-alice").Error)
			require.Equal(t, "tenant-acme", user.TenantID)

			require.NoError(t, adapter.MigrateDatabase(t.Context(), measured, artifact))
			require.EqualValues(t, 2, counter.foreignKeyChecks.Load())
			require.NoError(t, adapter.CheckDatabaseSchema(t.Context(), db))
			require.NoError(t, db.Raw("PRAGMA foreign_keys").Scan(&restored).Error)
			require.Equal(t, enabled, restored)
		})
	}
}
