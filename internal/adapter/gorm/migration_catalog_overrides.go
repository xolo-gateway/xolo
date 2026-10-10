package gorm

import (
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

const catalogOverridesMigrationID = "202610150001"

// migrateCatalogOverrides adds the overrides_json column to the org and
// personal virtual models. Existing rows keep an empty value, which reads as
// no overrides.
func migrateCatalogOverrides(tx *gorm.DB) error {
	return errors.WithStack(tx.AutoMigrate(&VirtualModel{}, &PersonalVirtualModel{}))
}
