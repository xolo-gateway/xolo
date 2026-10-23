package gorm

import (
	"log/slog"

	"gorm.io/gorm"
)

const llmModelProjectionsMigrationID = "202610230001"

// migrateLLMModelProjections projects the existing models of providers,
// which became a read-only business family. Like the other backfills, it
// emits no event: consumers read the existing state through an inventory.
func migrateLLMModelProjections(tx *gorm.DB) error {
	slog.WarnContext(tx.Statement.Context, "migration "+llmModelProjectionsMigrationID+" requires ALL old servers to be stopped: an old server writes models without publishing, so their projections, ETags and events would silently diverge")
	if !tx.Migrator().HasTable(resourceTables["llm_model"]) {
		return nil
	}
	return backfillProjections(tx, "llm_model")
}
