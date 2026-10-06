package gorm

import (
	"fmt"

	"gorm.io/gorm"
)

const recoveryPageSize = 1000
const recoveryInsertBatchSize = 250

// The tables belong to the migration connection and transaction. A rollback
// removes their DDL; successful application drops them before the checkpoint.
func installRecoveryMappings(tx *gorm.DB, a *RecoveryArtifact) error {
	if err := tx.Exec(`CREATE TEMPORARY TABLE uuid_recovery_ids (
  family text NOT NULL, old_id text NOT NULL, new_id text NOT NULL,
  PRIMARY KEY (family, old_id)
 )`).Error; err != nil {
		return err
	}
	type mapping struct{ Family, OldID, NewID string }
	rows := make([]mapping, 0, recoveryInsertBatchSize)
	flush := func() error {
		if len(rows) == 0 {
			return nil
		}
		err := tx.Table("uuid_recovery_ids").Create(&rows).Error
		rows = rows[:0]
		return err
	}
	for _, family := range []string{"tenants", "organizations", "users"} {
		for old, next := range a.IDs[family] {
			if old == next {
				continue
			}
			rows = append(rows, mapping{Family: family, OldID: old, NewID: next})
			if len(rows) == recoveryInsertBatchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return tx.Exec(`CREATE TEMPORARY TABLE uuid_recovery_changes (
  id text PRIMARY KEY, before_value text NOT NULL, after_value text NOT NULL
 )`).Error
}

func rewriteRecoveryReference(tx *gorm.DB, ref recoveryReference) error {
	table, column := tx.Statement.Quote(ref.table), tx.Statement.Quote(ref.column)
	// The correlated lookup uses the mapping's primary key. Even an unindexed
	// historical reference scans its table once, independent of mapping size.
	lookup := ` FROM uuid_recovery_ids AS mapping
  WHERE mapping.family = ? AND mapping.old_id = substr(` + table + "." + column + `, ?)`
	query := "UPDATE " + table + " SET " + column + " = (SELECT ? || mapping.new_id" + lookup + ")" +
		" WHERE EXISTS (SELECT 1" + lookup + ")"
	if ref.where != "" {
		query += " AND (" + ref.where + ")"
	}
	prefixStart := len(ref.prefix) + 1
	return tx.Exec(query, ref.prefix, ref.family, prefixStart, ref.family, prefixStart).Error
}

func applySerializedRecoveryBatch(tx *gorm.DB, changes []RecoverySerializedOverride) error {
	if len(changes) == 0 {
		return nil
	}
	if err := tx.Exec("DELETE FROM uuid_recovery_changes").Error; err != nil {
		return err
	}
	type correction struct{ ID, BeforeValue, AfterValue string }
	rows := make([]correction, 0, len(changes))
	for _, change := range changes {
		rows = append(rows, correction{ID: change.ID, BeforeValue: change.Before, AfterValue: change.After})
	}
	if err := tx.Table("uuid_recovery_changes").CreateInBatches(&rows, recoveryInsertBatchSize).Error; err != nil {
		return err
	}
	table, column := tx.Statement.Quote(changes[0].Table), tx.Statement.Quote(changes[0].Column)
	result := tx.Exec("UPDATE " + table + " SET " + column +
		" = (SELECT after_value FROM uuid_recovery_changes WHERE id = " + table + ".id)" +
		" WHERE id IN (SELECT id FROM uuid_recovery_changes) AND COALESCE(" + column + ", '')" +
		" = (SELECT before_value FROM uuid_recovery_changes WHERE id = " + table + ".id)")
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != int64(len(changes)) {
		return fmt.Errorf("serialized recovery changed during application; regenerate the plan")
	}
	return nil
}
