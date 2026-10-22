package gorm

import (
	"log/slog"

	"gorm.io/gorm"
)

const pipelineProjectionsMigrationID = "202610220003"

// migratePipelineProjections projects the existing virtual models and
// middlewares, which became business families. Like the other backfills, it
// emits no event: consumers read the existing state through an inventory.
func migratePipelineProjections(tx *gorm.DB) error {
	slog.WarnContext(tx.Statement.Context, "migration "+pipelineProjectionsMigrationID+" requires ALL old servers to be stopped: an old server writes virtual models and middlewares without publishing, so their projections, ETags and events would silently diverge")
	for _, kind := range []string{"virtual_model", "middleware"} {
		if !tx.Migrator().HasTable(resourceTables[kind]) {
			continue
		}
		if err := backfillProjections(tx, kind); err != nil {
			return err
		}
	}
	return nil
}
