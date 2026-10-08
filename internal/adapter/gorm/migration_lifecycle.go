package gorm

import (
	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const lifecycleMigrationID = "202610130001"

// migrateLifecycle creates the deletion records and the guard control. It
// installs no guard: PrepareLifecycle does, at startup, only when the
// lifecycle is enabled.
func migrateLifecycle(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&ResourceDeletion{}, &LifecycleControl{}); err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&LifecycleControl{ID: lifecycleControlID}).Error)
}
