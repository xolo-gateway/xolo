package gorm

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func migrateBusiness(db *gorm.DB) error {
	for _, kind := range []string{"role", "application", "quota", "alert", "provider"} {
		var ids []string
		if err := db.Table(resourceTables[kind]).Order("id").Pluck("id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			key := mutationKey{kind, id}
			raw, err := mutationSnapshot(db, key)
			if err != nil {
				return err
			}
			rec, err := businessProjection(db, key, raw)
			if err != nil {
				return err
			}
			if rec == nil {
				continue
			}
			var row struct{ UpdatedAt time.Time }
			if err = db.Table(resourceTables[kind]).Where("id = ?", id).Select("updated_at").Scan(&row).Error; err != nil {
				return err
			}
			rec.UpdatedAt = row.UpdatedAt
			if rec.UpdatedAt.IsZero() {
				rec.UpdatedAt = db.NowFunc().UTC()
			}
			if err = db.Clauses(clause.OnConflict{DoNothing: true}).Create(rec).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
