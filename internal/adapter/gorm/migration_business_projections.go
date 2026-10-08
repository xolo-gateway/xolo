package gorm

import (
	"log/slog"

	"gorm.io/gorm"
)

const businessProjectionsMigrationID = "202610120001"

// migrateBusinessProjections projects the existing business resources:
// custom roles, applications, quotas, alerts and providers. Like the first
// backfill, it emits no event: consumers read the existing state through an
// inventory. A resource without parent is left unprojected.
func migrateBusinessProjections(tx *gorm.DB) error {
	slog.WarnContext(tx.Statement.Context, "migration "+businessProjectionsMigrationID+" requires ALL old servers to be stopped: an old server writes business resources without publishing, so their projections, ETags and events would silently diverge")
	for _, kind := range []string{"provider", "role", "application", "quota", "alert"} {
		// A database upgraded from a minimal schema may lack a table.
		if !tx.Migrator().HasTable(resourceTables[kind]) {
			continue
		}
		if err := backfillProjections(tx, kind); err != nil {
			return err
		}
	}
	return nil
}
