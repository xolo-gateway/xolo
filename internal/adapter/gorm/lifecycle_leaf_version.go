package gorm

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A leaf key survives DELETE/recreation. Its version must too, even if the
// clock has not advanced and the source publication was already pruned.
type LeafVersion struct {
	Family         string    `gorm:"primaryKey"`
	TenantID       string    `gorm:"primaryKey"`
	OrganizationID string    `gorm:"primaryKey"`
	Key            string    `gorm:"primaryKey"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime:false"`
}

func advanceLeafVersion(db *gorm.DB, rec *CommonRecord) error {
	var old LeafVersion
	err := db.Where("family = ? AND tenant_id = ? AND organization_id = ? AND key = ?", rec.Family, rec.TenantID, rec.OrganizationID, rec.Key).First(&old).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if !rec.UpdatedAt.After(old.UpdatedAt) {
		rec.UpdatedAt = old.UpdatedAt.Add(time.Microsecond)
	}
	return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&LeafVersion{Family: rec.Family, TenantID: rec.TenantID, OrganizationID: rec.OrganizationID, Key: rec.Key, UpdatedAt: rec.UpdatedAt}).Error
}
