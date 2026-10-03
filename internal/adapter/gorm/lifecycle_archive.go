package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type deletionPayload struct {
	Format   string                      `json:"format"`
	Source   string                      `json:"source"`
	Cursor   string                      `json:"c0"`
	Deletion model.Deletion              `json:"deletion"`
	Tables   map[string][]map[string]any `json:"tables"`
	Count    int                         `json:"record_count"`
	Complete bool                        `json:"complete"`
}
type deletionEnvelope struct {
	Payload json.RawMessage `json:"payload"`
	SHA256  string          `json:"sha256"`
}

func readArchiveRows(q *gorm.DB) ([]map[string]any, error) {
	rows := []map[string]any{}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		for k, v := range row {
			if b, ok := v.([]byte); ok {
				row[k] = string(b)
			}
		}
	}
	// Sort by the entire canonical JSON row, including composite-key tables.
	sort.Slice(rows, func(i, j int) bool {
		a, _ := json.Marshal(rows[i])
		b, _ := json.Marshal(rows[j])
		return string(a) < string(b)
	})
	return rows, nil
}
func ownedEvent(d ResourceDeletion, e model.CommonEvent) bool {
	if e.Data.Key.TenantID != d.TenantID {
		return false
	}
	switch d.Family {
	case "tenant":
		return true
	case "organization":
		return e.Data.Key.OrganizationID == d.ResourceID
	case "member":
		return e.Data.Key.MemberID == d.ResourceID
	}
	return false
}
func (s *Store) deletionInventory(ctx context.Context, d ResourceDeletion) (map[string][]map[string]any, error) {
	db, _ := s.getDatabase(ctx)
	tables := map[string][]map[string]any{}
	ids := map[string]map[string]bool{}
	for _, t := range lifecycleTables(db) {
		rows, err := readArchiveRows(lifecycleQuery(db, t, d))
		if err != nil {
			return nil, err
		}
		tables[t.table] = rows
		ids[t.table] = map[string]bool{}
		for _, row := range rows {
			if id, ok := row["id"].(string); ok {
				ids[t.table][id] = true
			}
		}
	}
	// Publication and materialized deliveries are independently selected by their
	// immutable event keys: source feed retention does not hide delivery copies.
	for _, table := range []string{"publications", "webhook_deliveries"} {
		rows, err := readArchiveRows(db.Table(table).Where("tenant_id = ?", d.TenantID))
		if err != nil {
			return nil, err
		}
		tables[table] = []map[string]any{}
		col := "payload"
		if table == "webhook_deliveries" {
			col = "body"
		}
		for _, row := range rows {
			var e model.CommonEvent
			raw, _ := row[col].(string)
			if err := json.Unmarshal([]byte(raw), &e); err != nil {
				return nil, err
			}
			if ownedEvent(d, e) || ids["users"][e.Data.Key.MemberID] {
				tables[table] = append(tables[table], row)
			}
		}
	}
	tables["webhook_subscriptions"] = []map[string]any{}
	if d.Family == "tenant" {
		rows, err := readArchiveRows(db.Table("webhook_subscriptions").Where("tenant_id = ?", d.TenantID))
		if err != nil {
			return nil, err
		}
		tables["webhook_subscriptions"] = rows
		ids["webhook_subscriptions"] = map[string]bool{}
		for _, r := range rows {
			id, _ := r["id"].(string)
			ids["webhook_subscriptions"][id] = true
		}
	}
	tables["common_records"] = []map[string]any{}
	rows, err := readArchiveRows(db.Table("common_records").Where("tenant_id = ?", d.TenantID))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		var key model.CommonKey
		raw, _ := row["reference"].(string)
		if err := json.Unmarshal([]byte(raw), &key); err != nil {
			return nil, err
		}
		if ownedEvent(d, model.CommonEvent{Data: model.CommonEventData{Key: key}}) || ids["users"][key.MemberID] {
			tables["common_records"] = append(tables["common_records"], row)
		}
	}
	tables["mutation_audits"] = []map[string]any{}
	rows, err = readArchiveRows(db.Table("mutation_audits"))
	if err != nil {
		return nil, err
	}

	// Historical facts can outlive their resource rows. Resolve their scope
	// transitively (tenant -> org -> application -> quota) before erasing them.
	selected := make([]bool, len(rows))
	emails := map[string]bool{}
	for _, u := range tables["users"] {
		if email, ok := u["email"].(string); ok {
			emails[strings.ToLower(email)] = true
		}
	}
	changed := true
	for changed {
		changed = false
		for i, row := range rows {
			if selected[i] {
				continue
			}
			kind, _ := row["resource"].(string)
			id, _ := row["resource_id"].(string)
			table := resourceTables[kind]
			if kind == "subscription" {
				table = "webhook_subscriptions"
			}
			owned := ids[table][id]
			for _, col := range []string{"before", "after"} {
				raw, _ := row[col].(string)
				var v map[string]any
				if err := json.Unmarshal([]byte(raw), &v); err != nil {
					return nil, err
				}
				str := func(key string) string { value, _ := v[key].(string); return value }
				switch d.Family {
				case "tenant":
					owned = owned || str("tenant_id") == d.ResourceID || kind == "tenant" && id == d.ResourceID || ids["organizations"][str("org_id")] || ids["users"][str("user_id")]
				case "organization":
					owned = owned || str("org_id") == d.ResourceID || kind == "organization" && id == d.ResourceID
				case "member":
					owned = owned || str("user_id") == d.ResourceID || str("owner_id") == d.ResourceID || kind == "member" && id == d.ResourceID
					if kind == "invitation" {
						owned = owned || str("created_by_user_id") == d.ResourceID || str("tenant_id") == d.TenantID && emails[strings.ToLower(str("invitee_email"))]
					}
				}
				if kind == "quota" {
					owned = owned || str("scope") == "application" && ids["applications"][str("scope_id")] || str("scope") == "org" && ids["organizations"][str("scope_id")] || str("scope") == "user" && ids["users"][str("scope_id")]
				}
			}
			if owned {
				selected[i] = true
				changed = true
				if table != "" {
					if ids[table] == nil {
						ids[table] = map[string]bool{}
					}
					ids[table][id] = true
				}
			}
		}
	}
	for i, row := range rows {
		if selected[i] {
			tables["mutation_audits"] = append(tables["mutation_audits"], row)
		}
	}

	tables["resource_deletions"], err = readArchiveRows(db.Table("resource_deletions").Where("tenant_id = ?", d.TenantID))
	if err != nil {
		return nil, err
	}
	filtered := tables["resource_deletions"][:0]
	for _, row := range tables["resource_deletions"] {
		family, _ := row["family"].(string)
		id, _ := row["resource_id"].(string)
		if d.Family == "tenant" || family == d.Family && id == d.ResourceID || family == "member" && ids["users"][id] {
			filtered = append(filtered, row)
		}
	}
	tables["resource_deletions"] = filtered
	// Sessions are global per issuer/subject. Only unshared identities are part
	// of this footprint; another tenant's session must survive a member purge.
	tables["identity_sessions"] = []map[string]any{}
	keys := map[string]bool{}
	for _, row := range tables["users"] {
		issuer, _ := row["identity_issuer"].(string)
		subject, _ := row["identity_subject"].(string)
		provider, _ := row["provider"].(string)
		linkedSubject, _ := row["subject"].(string)
		if issuer == "" {
			issuer = s.identityIssuer(provider)
			subject = linkedSubject
		}
		if issuer == "" || subject == "" {
			continue
		}
		var users []User
		if err := db.Where("(identity_issuer = ? AND identity_subject = ?) OR (provider IN ? AND subject = ?)", issuer, subject, s.identityAliases(issuer), subject).Find(&users).Error; err != nil {
			return nil, err
		}
		shared := false
		for _, u := range users {
			if !ids["users"][u.ID] {
				shared = true
			}
		}
		if !shared {
			keys[identityDigest(issuer, subject)] = true
		}
	}
	for key := range keys {
		rows, err := readArchiveRows(db.Table("identity_sessions").Where("identity_key = ?", key))
		if err != nil {
			return nil, err
		}
		tables["identity_sessions"] = append(tables["identity_sessions"], rows...)
	}
	return tables, nil
}
func (s *Store) ExportDeletion(ctx context.Context, scope model.CommonScope, key string) ([]byte, error) {
	var out []byte
	err := s.identityTransaction(ctx, func(tx *Store) error {
		db, _ := tx.getDatabase(ctx)
		d, err := deletionRow(db, scope, key)
		if err != nil {
			return err
		}
		if d.PurgedAt != nil {
			return port.ErrNotFound
		}
		cursor, err := tx.CaptureCommonCursor(ctx)
		if err != nil {
			return err
		}
		var feed CommonFeed
		if err = db.First(&feed, 1).Error; err != nil {
			return err
		}
		tables, err := tx.deletionInventory(ctx, d)
		if err != nil {
			return err
		}
		payload := deletionPayload{Format: "xolo-deletion/1", Source: feed.Source, Cursor: cursor, Deletion: d.view(), Tables: tables, Complete: true}
		for _, rows := range tables {
			payload.Count += len(rows)
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		hash := hex.EncodeToString(digest[:])
		// Only a fully encoded archive receives a receipt candidate. This records
		// generation, never acknowledgement of external durable receipt.
		if err = db.Clauses(clause.OnConflict{DoNothing: true}).Create(&DeletionArchive{Family: d.Family, ResourceID: key, ETag: d.ETag, SHA256: hash}).Error; err != nil {
			return err
		}
		out, err = json.Marshal(deletionEnvelope{Payload: raw, SHA256: hash})
		return err
	})
	return out, err
}
func (s *Store) ConfirmDeletion(ctx context.Context, scope model.CommonScope, key string, c model.MatchCondition, digest string) (model.Deletion, error) {
	var out model.Deletion
	if !c.Present || c.Any || len(c.Tags) == 0 {
		return out, port.ErrConfirmationRequired
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != digest {
		return out, port.ErrInvalid
	}
	if err = s.checkOwnership(ctx, scope.Family); err != nil {
		return out, err
	}
	err = s.identityTransaction(ctx, func(tx *Store) error {
		db, _ := tx.getDatabase(ctx)
		d, err := deletionRow(db, scope, key)
		if err != nil {
			return err
		}
		if !c.Matches(d.ETag) {
			return port.ErrPreconditionFailed
		}
		if d.ConfirmedAt != nil {
			if d.ExportSHA256 != digest {
				return port.ErrExportMismatch
			}
			out = d.view()
			return nil
		}
		var n int64
		if err = db.Model(&DeletionArchive{}).Where("family = ? AND resource_id = ? AND etag = ? AND sha256 = ?", d.Family, key, d.ETag, digest).Count(&n).Error; err != nil {
			return err
		}
		if n != 1 {
			return port.ErrExportMismatch
		}
		now := db.NowFunc().UTC()
		d.ConfirmedAt = &now
		d.ExportSHA256 = digest
		if err = db.Save(&d).Error; err != nil {
			return err
		}
		item, err := readCommon(db, scope, key)
		if err != nil {
			return err
		}
		if err = tx.auditControlOperation(ctx, d.Family, key, "export_confirmed"); err != nil {
			return err
		}
		if err = publishExtension(model.WithActor(ctx, tx.mutations.actor), db, d.Family, item, "export_confirmed"); err != nil {
			return err
		}
		out = d.view()
		return nil
	})
	return out, err
}
func (s *Store) DueDeletions(ctx context.Context, limit int) ([]model.Deletion, error) {
	out := []model.Deletion{}
	if limit < 1 || limit > 100 {
		return nil, port.ErrInvalid
	}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return nil, err
	}
	var rows []ResourceDeletion
	err = db.WithContext(ctx).Where("confirmed_at IS NOT NULL AND purged_at IS NULL AND purge_after <= ?", time.Now().UTC()).Where("family = 'tenant' OR NOT EXISTS (SELECT 1 FROM resource_deletions p WHERE p.family = 'tenant' AND p.resource_id = resource_deletions.tenant_id)").Where("family <> 'member' OR NOT EXISTS (SELECT 1 FROM memberships m JOIN resource_deletions p ON p.family = 'organization' AND p.resource_id = m.org_id WHERE m.user_id = resource_deletions.resource_id)").Order("attempts, purge_after, family, resource_id").Limit(limit).Find(&rows).Error
	for _, d := range rows {
		out = append(out, d.view())
	}
	return out, err
}
