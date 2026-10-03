package gorm

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm/clause"
)

func archiveRowKey(table string, row map[string]any) map[string]any {
	fields := []string{"id"}
	switch table {
	case "membership_roles":
		fields = []string{"membership_id", "role_id"}
	case "application_roles":
		fields = []string{"application_id", "role_id"}
	case "role_permissions":
		fields = []string{"role_id", "code"}
	case "role_models":
		fields = []string{"role_id", "model_id", "model_kind"}
	case "quota_usages":
		fields = []string{"scope", "scope_id", "org_id", "currency", "day"}
	case "domains":
		fields = []string{"hostname"}
	case "common_records", "leaf_versions":
		fields = []string{"family", "tenant_id", "organization_id", "key"}
	case "mutation_audits", "publications":
		fields = []string{"sequence"}
	case "event_settings":
		fields = []string{"org_id"}
	}
	out := map[string]any{}
	for _, f := range fields {
		out[f] = row[f]
	}
	return out
}
func (s *Store) PurgeDeletion(ctx context.Context, scope model.CommonScope, key string) error {
	err := s.identityTransaction(ctx, func(tx *Store) error {
		db, _ := tx.getDatabase(ctx)
		d, err := deletionRow(db, scope, key)
		if err != nil {
			return err
		}
		if d.PurgedAt != nil {
			return nil
		}
		if d.ConfirmedAt == nil || db.NowFunc().Before(d.PurgeAfter) {
			return port.ErrPurgeNotReady
		}
		if d.Family != "tenant" {
			var n int64
			if err = db.Model(&ResourceDeletion{}).Where("family = 'tenant' AND resource_id = ?", d.TenantID).Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				return port.ErrPurgeNotReady
			}
		}
		if d.Family == "member" {
			var n int64
			if err = db.Table("memberships m").Joins("JOIN resource_deletions d ON d.family = 'organization' AND d.resource_id = m.org_id").Where("m.user_id = ?", key).Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				return port.ErrPurgeNotReady
			}
		}
		var archive int64
		if err = db.Model(&DeletionArchive{}).Where("family = ? AND resource_id = ? AND etag = ? AND sha256 = ?", d.Family, key, d.ETag, d.ExportSHA256).Count(&archive).Error; err != nil {
			return err
		}
		if archive != 1 {
			return port.ErrExportMismatch
		}
		item, err := readCommon(db, scope, key)
		if err != nil {
			return err
		}
		tables, err := tx.deletionInventory(ctx, d)
		if err != nil {
			return err
		}
		// Bypass is protected by the clock, rolled back on error, and never committed.
		if err = db.Model(&LifecycleControl{}).Where("id = 1").Update("bypass", 1).Error; err != nil {
			return err
		}
		if err = tx.retireLoginRows(db, tables["users"]); err != nil {
			return err
		}
		// Capture all IDs before any parent disappears. No cascade defines scope.
		order := []string{"identity_sessions", "webhook_deliveries", "webhook_subscriptions", "mutation_audits", "publications", "common_records"}
		for _, t := range lifecycleTables(db) {
			order = append(order, t.table)
		}
		var floor int64
		for _, row := range tables["publications"] {
			switch seq := row["sequence"].(type) {
			case int64:
				floor = max(floor, seq)
			case int:
				floor = max(floor, int64(seq))
			}
		}
		// Shared organization alerts remain; their deleted creator is detached.
		for _, row := range tables["users"] {
			var alerts []Alert
			if err = db.Where("owner_id = ? AND scope <> 'personal'", row["id"]).Find(&alerts).Error; err != nil {
				return err
			}
			for _, alert := range alerts {
				removed := false
				for _, erased := range tables["alerts"] {
					if erased["id"] == alert.ID {
						removed = true
						break
					}
				}
				if removed {
					continue
				}
				key := mutationKey{"alert", alert.ID}
				before, e := mutationSnapshot(db, key)
				if e != nil {
					return e
				}
				if e = db.Model(&Alert{}).Where("id = ?", alert.ID).Update("owner_id", "").Error; e != nil {
					return e
				}
				after, e := mutationSnapshot(db, key)
				if e != nil {
					return e
				}
				// Redaction is part of the confirmed erasure, not a new actor's
				// configuration write. Keep its public projection coherent.
				if e = publishCommon(ctx, db, key, before, after, false, tx.businessEnabled); e != nil {
					return e
				}
			}
			// Remaining audits identify an erased actor, without altering other facts.
			var audits []MutationAudit
			if err = db.Find(&audits).Error; err != nil {
				return err
			}
			for _, a := range audits {
				var actor model.Actor
				if json.Unmarshal([]byte(a.Actor), &actor) == nil && string(actor.UserID) == row["id"] {
					actor.UserID = ""
					actor.URI = "urn:xolo:actor:erased"
					raw, e := json.Marshal(actor)
					if e != nil {
						return e
					}
					if e = db.Model(&MutationAudit{}).Where("sequence = ?", a.Sequence).Update("actor", string(raw)).Error; e != nil {
						return e
					}
				}
			}
		}
		for _, table := range order {
			for _, row := range tables[table] {
				if err = db.Table(table).Where(archiveRowKey(table, row)).Delete(map[string]any{}).Error; err != nil {
					return err
				}
			}
		}
		if floor > 0 {
			if err = db.Model(&CommonFeed{}).Where("id = 1 AND floor < ?", floor).Update("floor", floor).Error; err != nil {
				return err
			}
			if err = db.Model(&WebhookSubscription{}).Where("position < ?", floor).Update("state", "history_lost").Error; err != nil {
				return err
			}
		}
		now := db.NowFunc().UTC()
		for family, table := range map[string]string{"organization": "organizations", "member": "users", "application": "applications", "custom_role": "roles", "membership": "memberships", "alert": "alerts"} {
			for _, row := range tables[table] {
				id, _ := row["id"].(string)
				child := ResourceDeletion{Family: family, ResourceID: id, TenantID: d.TenantID, ETag: d.ETag, DeletedAt: d.DeletedAt, PurgeAfter: d.PurgeAfter, PurgedAt: &now, Diagnostic: "purged_with_parent"}
				if err = db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "family"}, {Name: "resource_id"}}, DoUpdates: clause.Assignments(map[string]any{"purged_at": now, "diagnostic": "purged_with_parent"})}).Create(&child).Error; err != nil {
					return err
				}
			}
		}
		for _, row := range tables["resource_deletions"] {
			if err = db.Where("family = ? AND resource_id = ?", row["family"], row["resource_id"]).Delete(&DeletionArchive{}).Error; err != nil {
				return err
			}
		}
		d.PurgedAt = &now
		d.Attempts++
		d.Diagnostic = "purged"
		if err = db.Where("family = ? AND resource_id = ?", d.Family, key).Delete(&DeletionArchive{}).Error; err != nil {
			return err
		}
		if err = db.Save(&d).Error; err != nil {
			return err
		}
		// Parent receipt supersedes every planned descendant. Minimal tombstones
		// reserve their retired UUIDs and make retries stable after physical purge.
		if d.Family == "tenant" {
			if err = db.Model(&ResourceDeletion{}).Where("tenant_id = ? AND purged_at IS NULL", d.TenantID).Updates(map[string]any{"purged_at": now, "diagnostic": "purged_with_parent"}).Error; err != nil {
				return err
			}
		}
		if err = tx.auditControlOperation(ctx, d.Family, key, "purged"); err != nil {
			return err
		}
		if err = publishExtension(model.WithActor(ctx, tx.mutations.actor), db, d.Family, item, "purged"); err != nil {
			return err
		}
		return db.Model(&LifecycleControl{}).Where("id = 1").Update("bypass", 0).Error
	})
	if err != nil && !errors.Is(err, port.ErrPurgeNotReady) && !errors.Is(err, port.ErrNotFound) && ctx.Err() == nil {
		// Separate diagnostic transaction: never commit any portion of a failed purge.
		diagnosticErr := s.identityTransaction(ctx, func(tx *Store) error {
			db, _ := tx.getDatabase(ctx)
			d, e := deletionRow(db, scope, key)
			if e != nil {
				return e
			}
			if d.PurgedAt != nil {
				return nil
			}
			d.Attempts++
			d.Diagnostic = "purge_failed"
			return db.Save(&d).Error
		})
		if diagnosticErr != nil {
			return errors.Join(err, diagnosticErr)
		}
	}
	return err
}

var _ port.LifecycleStore = (*Store)(nil)
