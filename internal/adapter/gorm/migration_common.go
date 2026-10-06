package gorm

import (
	"fmt"
	"log/slog"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"gorm.io/gorm"
)

const commonMigrationID = "202610020001"

// requireCommonIDs checks the post-migration identifier format.
func requireCommonIDs(db *gorm.DB) error {
	for _, table := range []string{"tenants", "organizations", "users"} {
		if !db.Migrator().HasTable(table) {
			continue
		}
		var ids []string
		if err := db.Table(table).Pluck("id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := model.ParseTenantID(id); err != nil {
				return fmt.Errorf("legacy identifiers remain in %s", table)
			}
		}
	}
	return nil
}
func migrateCommonSchema(tx *gorm.DB) error {
	// The migration lock covers planning, reference conversion and the marker.
	warnCommonMigration(tx)
	a, err := PlanCommonRecovery(tx.Statement.Context, tx)
	if err != nil {
		return err
	}
	_, err = applyCommonRecovery(tx.Statement.Context, tx, a)
	return err
}

func warnCommonMigration(tx *gorm.DB) {
	slog.WarnContext(tx.Statement.Context, "UUID migration 202610020001 requires ALL old servers, workers and writers to be stopped; rolling upgrades are unsupported. Back up the database first; old replicas would create orphaned usage and quota records.")
}
