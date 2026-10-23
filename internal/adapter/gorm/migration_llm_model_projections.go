package gorm

import (
	"log/slog"

	"gorm.io/gorm"
)

const llmModelProjectionsMigrationID = "202610230001"

// migrateLLMModelProjections projects the existing models of providers and
// personal virtual models, which became read-only business families. Like
// the other backfills, it emits no event: consumers read the existing state
// through an inventory.
func migrateLLMModelProjections(tx *gorm.DB) error {
	slog.WarnContext(tx.Statement.Context, "migration "+llmModelProjectionsMigrationID+" requires ALL old servers to be stopped: an old server writes models and personal virtual models without publishing, so their projections, ETags and events would silently diverge")
	for _, kind := range []string{"llm_model", "personal_virtual_model"} {
		if !tx.Migrator().HasTable(resourceTables[kind]) {
			continue
		}
		if err := backfillProjections(tx, kind); err != nil {
			return err
		}
	}
	return nil
}
