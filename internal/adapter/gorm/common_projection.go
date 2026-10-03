package gorm

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CommonRecord is a transactional read projection. UpdatedAt belongs exclusively
// to the common representation, independent of login and application metadata.
type CommonRecord struct {
	Family         string    `gorm:"primaryKey"`
	TenantID       string    `gorm:"primaryKey"`
	OrganizationID string    `gorm:"primaryKey"`
	Key            string    `gorm:"primaryKey"`
	Reference      string    `gorm:"type:text"`
	Representation string    `gorm:"type:text"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime:false"`
}

// CommonFeed stores instance identity, cursor authentication material and the
// durable loss boundary even when all publications have been removed.
type CommonFeed struct {
	ID           int `gorm:"primaryKey;autoIncrement:false"`
	Source       string
	CursorSecret string
	Floor        int64
}

func migrateCommonReads(db *gorm.DB) error {
	if err := db.AutoMigrate(&CommonRecord{}, &CommonFeed{}); err != nil {
		return err
	}
	feed := CommonFeed{ID: 1, Source: "urn:uuid:" + uuid.NewString(), CursorSecret: uuid.NewString() + uuid.NewString()}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&feed).Error; err != nil {
		return err
	}
	// Legacy outbox rows contain internal snapshots, not public CloudEvents.
	// A fresh feed starts after these historical facts; lists recover all state.
	var clock PublicationClock
	if err := db.First(&clock, 1).Error; err != nil {
		return err
	}
	if err := db.Model(&CommonFeed{}).Where("id = 1").Update("floor", clock.Sequence).Error; err != nil {
		return err
	}
	if err := db.Where("sequence <= ?", clock.Sequence).Delete(&Publication{}).Error; err != nil {
		return err
	}
	for _, kind := range []string{"tenant", "organization", "member", "domain", "membership"} {
		col := "id"
		if kind == "domain" {
			col = "hostname"
		}
		var ids []string
		if err := db.Table(resourceTables[kind]).Pluck(col, &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			key := mutationKey{kind, id}
			snapshot, err := mutationSnapshot(db, key)
			if err != nil {
				return err
			}
			rec, err := commonProjection(db, key, snapshot)
			if err != nil {
				return err
			}
			var timestamp struct{ UpdatedAt time.Time }
			// Memberships have CreatedAt only. Their first public validator is
			// assigned at migration, as for domains; reading updated_at would
			// break upgrades of any instance with an existing membership.
			if kind != "domain" && kind != "membership" {
				if err := db.Table(resourceTables[kind]).Select("updated_at").Where(col+" = ?", id).Scan(&timestamp).Error; err != nil {
					return err
				}
			}
			rec.UpdatedAt = timestamp.UpdatedAt
			if rec.UpdatedAt.IsZero() {
				rec.UpdatedAt = db.NowFunc().UTC()
			}
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(rec).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
func commonProjection(db *gorm.DB, key mutationKey, raw []byte) (*CommonRecord, error) {
	if model.IsBusinessFamily(businessFamily(key.kind)) {
		return businessProjection(db, key, raw)
	}
	if key.kind != "tenant" && key.kind != "organization" && key.kind != "member" && key.kind != "domain" && key.kind != "membership" {
		return nil, nil
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	str := func(k string) string { v, _ := row[k].(string); return v }
	tid, oid := str("tenant_id"), str("org_id")
	if key.kind == "tenant" {
		tid = key.id
	}
	if key.kind == "organization" {
		oid = key.id
	}
	if tid == "" {
		return nil, port.ErrParentNotFound
	}
	var count int64
	if err := db.Table("tenants").Where("id = ?", tid).Count(&count).Error; err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, port.ErrParentNotFound
	}
	if oid != "" {
		if err := db.Table("organizations").Where("id = ? AND tenant_id = ?", oid, tid).Count(&count).Error; err != nil {
			return nil, err
		}
		if count != 1 {
			return nil, port.ErrParentNotFound
		}
	}
	ref := model.CommonKey{TenantID: tid}
	rec := &CommonRecord{Family: key.kind, TenantID: tid, Key: key.id}
	status := str("status")
	if key.kind == "tenant" || key.kind == "organization" || key.kind == "member" {
		status = "suspended"
		if row["active"] == true || row["active"] == float64(1) {
			status = "active"
		}
	}
	rep := map[string]any{"status": status}
	switch key.kind {
	case "tenant", "organization":
		rep["slug"], rep["name"] = str("slug"), str("name")
		if key.kind == "organization" {
			ref.OrganizationID = key.id
		}
	case "member":
		ref.MemberID = key.id
		if str("identity_issuer") != "" {
			rep["identity"] = model.Identity{Issuer: str("identity_issuer"), Subject: str("identity_subject")}
		}
		rep["email"] = str("email")
		rep["tenant_role"] = str("tenant_role")
		if name := str("display_name"); name != "" {
			rep["display_name"] = name
		}
	case "domain":
		rec.Family = "tenant_domain"
		ref.Hostname = key.id
	case "membership":
		if err := db.Table("users").Where("id = ? AND tenant_id = ?", str("user_id"), tid).Count(&count).Error; err != nil {
			return nil, err
		}
		if count != 1 {
			return nil, port.ErrParentNotFound
		}
		rec.Family = "organization_membership"
		rec.OrganizationID = oid
		rec.Key = str("user_id")
		ref.OrganizationID = oid
		ref.MemberID = rec.Key
		rep["role"] = str("common_role")
	}
	encoded, err := json.Marshal(rep)
	if err != nil {
		return nil, err
	}
	rec.Representation = string(encoded)
	encoded, err = json.Marshal(ref)
	if err != nil {
		return nil, err
	}
	rec.Reference = string(encoded)
	return rec, nil
}
func recordQuery(db *gorm.DB, rec *CommonRecord) *gorm.DB {
	return db.Where("family = ? AND tenant_id = ? AND organization_id = ? AND key = ?", rec.Family, rec.TenantID, rec.OrganizationID, rec.Key)
}
func (r CommonRecord) item() (model.CommonItem, error) {
	out := model.CommonItem{Representation: json.RawMessage(r.Representation), ETag: model.CommonETag(r.UpdatedAt)}
	err := json.Unmarshal([]byte(r.Reference), &out.Key)
	return out, err
}

// publishCommon constructs the closed public profile from validated references,
// never from arbitrary audit attributes. Only this transaction path inserts it.
func publishCommon(ctx context.Context, db *gorm.DB, key mutationKey, before, after []byte, put, business bool) error {

	rec, err := commonProjection(db, key, after)
	if err != nil {
		return err
	}
	if rec == nil {
		// Deletion is a Xolo extension, outside the common draft's lifecycle.
		if string(after) == "null" {
			var old map[string]any
			if err := json.Unmarshal(before, &old); err != nil {
				return err
			}
			family := businessFamily(key.kind)
			id := key.id
			org := ""
			if model.IsBusinessFamily(family) && family != "quota" {
				org, _ = old["org_id"].(string)
			}
			if family == "domain" {
				family = "tenant_domain"
			}
			if family == "membership" {
				family = "organization_membership"
				id, _ = old["user_id"].(string)
				org, _ = old["org_id"].(string)
			}
			var previous CommonRecord
			query := db.Where("family = ? AND key = ? AND organization_id = ?", family, id, org)
			if err := query.First(&previous).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return nil
				}
				return err
			}
			item, err := previous.item()
			if err != nil {
				return err
			}
			if !model.IsBusinessFamily(family) || business {
				if err := publishExtension(ctx, db, family, item, "deleted"); err != nil {
					return err
				}
			}
			return db.Where("family = ? AND key = ? AND organization_id = ?", family, id, org).Delete(&CommonRecord{}).Error
		}
		return nil
	}
	var previous CommonRecord
	err = recordQuery(db, rec).First(&previous).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	credentialChanged := false
	if key.kind == "provider" {
		var a, b map[string]any
		if err := json.Unmarshal(before, &a); err != nil {
			return err
		}
		if err := json.Unmarshal(after, &b); err != nil {
			return err
		}
		credentialChanged = a["api_key"] != b["api_key"]
	}
	if err == nil && previous.Representation == rec.Representation && !credentialChanged {
		return nil
	}
	var old map[string]any
	if err := json.Unmarshal(before, &old); err != nil {
		return err
	}
	if old != nil {
		if tid, _ := old["tenant_id"].(string); tid != "" && tid != rec.TenantID {
			return port.ErrNotAllowed
		}
	}
	rec.UpdatedAt = db.NowFunc().UTC().Truncate(time.Microsecond)
	if model.IsBusinessFamily(rec.Family) && !rec.UpdatedAt.After(previous.UpdatedAt) {
		rec.UpdatedAt = previous.UpdatedAt.Add(time.Microsecond)
	}
	if rec.Family == "tenant_domain" || rec.Family == "organization_membership" {
		if err := advanceLeafVersion(db, rec); err != nil {
			return err
		}
	}
	if err := db.Clauses(clause.OnConflict{UpdateAll: true}).Create(rec).Error; err != nil {
		return err
	}
	if model.IsBusinessFamily(rec.Family) && !business {
		return nil
	}
	types := []string{"updated"}
	if previous.Family == "" {
		types = []string{"created"}
		if !put && rec.Family == "organization_membership" {
			types = []string{"granted"}
		}
	} else if !put && !model.IsBusinessFamily(rec.Family) {
		var a, b map[string]any
		if err := json.Unmarshal([]byte(previous.Representation), &a); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(rec.Representation), &b); err != nil {
			return err
		}
		types = nil
		for _, field := range []string{"status", "tenant_role", "role"} {
			if a[field] != b[field] {
				types = append(types, field+"_changed")
			}
		}
		// Mutable text has no more specific local fact in the draft.
		aa, bb := map[string]any{}, map[string]any{}
		for k, v := range a {
			if k != "status" && k != "tenant_role" && k != "role" {
				aa[k] = v
			}
		}
		for k, v := range b {
			if k != "status" && k != "tenant_role" && k != "role" {
				bb[k] = v
			}
		}
		x, _ := json.Marshal(aa)
		y, _ := json.Marshal(bb)
		if !bytes.Equal(x, y) {
			types = append(types, "updated")
		}
	}
	var feed CommonFeed
	if err := db.First(&feed, 1).Error; err != nil {
		return err
	}
	item, err := rec.item()
	if err != nil {
		return err
	}
	for _, typ := range types {
		if err := db.Exec("UPDATE publication_clocks SET sequence = sequence + 1 WHERE id = 1").Error; err != nil {
			return err
		}
		var clock PublicationClock
		if err := db.First(&clock, 1).Error; err != nil {
			return err
		}
		event := model.CommonEvent{SpecVersion: "1.0", ID: uuid.NewString(), Source: feed.Source, Type: rec.Family + "." + typ + ".v1", Time: rec.UpdatedAt, DataContentType: "application/json", Sequence: strconv.FormatInt(clock.Sequence, 10), RequestID: model.ActorFromContext(ctx).RequestID, Data: model.CommonEventData{ResourceType: rec.Family, Key: item.Key, ETag: item.ETag}}
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if err := db.Create(&Publication{TenantID: rec.TenantID, OrgID: item.Key.OrganizationID, Sequence: clock.Sequence, CreatedAt: rec.UpdatedAt, Resource: rec.Family, ResourceID: rec.Key, Payload: string(payload)}).Error; err != nil {
			return err
		}
	}
	return nil
}

func validateCommonScope(scope model.CommonScope, key string) error {
	if scope.TenantID != "" {
		if _, err := model.ParseTenantID(scope.TenantID); err != nil {
			return port.ErrInvalid
		}
	}
	if scope.OrganizationID != "" {
		if _, err := model.ParseOrgID(scope.OrganizationID); err != nil {
			return port.ErrInvalid
		}
	}
	switch scope.Family {
	case "tenant":
		if scope.TenantID != "" || scope.OrganizationID != "" {
			return port.ErrInvalid
		}
	case "organization", "member", "tenant_domain", "quota":
		if scope.TenantID == "" || scope.OrganizationID != "" {
			return port.ErrInvalid
		}
	case "organization_membership", "custom_role", "application", "alert", "provider":
		if scope.TenantID == "" || scope.OrganizationID == "" {
			return port.ErrInvalid
		}
	default:
		return port.ErrInvalid
	}
	if key != "" {
		if model.IsBusinessFamily(scope.Family) {
			if !businessKey(key) {
				return port.ErrInvalid
			}
		} else if scope.Family == "tenant_domain" {
			host, err := model.NormalizeHostname(key)
			if err != nil || host != key {
				return port.ErrInvalidHostname
			}
		} else if _, err := model.ParseTenantID(key); err != nil {
			return port.ErrInvalid
		}
	}
	return nil
}
func commonParents(db *gorm.DB, scope model.CommonScope) error {
	var n int64
	if scope.TenantID != "" {
		if err := db.Table("tenants").Where("id = ?", scope.TenantID).Count(&n).Error; err != nil {
			return err
		}
		if n != 1 {
			return port.ErrParentNotFound
		}
	}
	if scope.OrganizationID != "" {
		if err := db.Table("organizations").Where("id = ? AND tenant_id = ?", scope.OrganizationID, scope.TenantID).Count(&n).Error; err != nil {
			return err
		}
		if n != 1 {
			return port.ErrParentNotFound
		}
	}
	return nil
}
func scopeQuery(db *gorm.DB, scope model.CommonScope) *gorm.DB {
	q := db.Model(&CommonRecord{}).Where("family = ?", scope.Family)
	if scope.TenantID != "" {
		q = q.Where("tenant_id = ?", scope.TenantID)
	}
	return q.Where("organization_id = ?", scope.OrganizationID)
}
func readCommon(db *gorm.DB, scope model.CommonScope, key string) (model.CommonItem, error) {
	if err := validateCommonScope(scope, key); err != nil {
		return model.CommonItem{}, err
	}
	if err := commonParents(db, scope); err != nil {
		return model.CommonItem{}, err
	}
	var row CommonRecord
	err := scopeQuery(db, scope).Where("key = ?", key).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		err = port.ErrNotFound
	}
	if err != nil {
		return model.CommonItem{}, err
	}
	return row.item()
}
func (s *Store) ReadCommon(ctx context.Context, scope model.CommonScope, key string) (model.CommonItem, error) {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return model.CommonItem{}, err
	}
	item, err := readCommon(db.WithContext(ctx), scope, key)
	if err == port.ErrParentNotFound {
		err = port.ErrNotFound
	}
	return item, err
}
func (s *Store) WriteCommon(ctx context.Context, scope model.CommonScope, key string, condition model.MatchCondition, fn func(port.ProvisioningTx) error) (model.CommonItem, error) {
	var out model.CommonItem
	if err := s.checkOwnership(ctx, scope.Family); err != nil {
		return out, err
	}
	err := s.identityTransaction(ctx, func(tx *Store) error {
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		if err := requireLive(db, scope, key); err != nil {
			return err
		}
		old, readErr := readCommon(db, scope, key)
		if readErr != nil && readErr != port.ErrNotFound && readErr != port.ErrParentNotFound {
			return readErr
		}
		tx.mutations.commonPUT = true
		tx.mutations.condition = &condition
		tx.mutations.etag = old.ETag
		// Services validate representations and parents first. mutate checks the
		// condition before the first write; the check below also covers no-ops.
		if err := fn(tx); err != nil {
			return err
		}
		if !condition.Matches(old.ETag) {
			return port.ErrPreconditionFailed
		}
		if err := tx.flushMutations(ctx, db); err != nil {
			return err
		}
		tx.mutations.before = map[mutationKey][]byte{}
		out, err = readCommon(db, scope, key)
		return err
	})
	return out, err
}

var _ port.CommonStore = (*Store)(nil)

// validateMutationScope also covers extension facts and immutable parent links.
// A caller cannot move a global ID between scopes through a lower-level store.
func validateMutationScope(db *gorm.DB, key mutationKey, before, after []byte) error {
	var old, current map[string]any
	if err := json.Unmarshal(before, &old); err != nil {
		return err
	}
	if err := json.Unmarshal(after, &current); err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	if old != nil {
		for _, field := range []string{"tenant_id", "org_id", "user_id"} {
			if old[field] != current[field] {
				return port.ErrNotAllowed
			}
		}
	}
	if key.kind == "role" || key.kind == "invitation" {
		var count int64
		if err := db.Table("organizations").Where("id = ? AND tenant_id = ?", current["org_id"], current["tenant_id"]).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return port.ErrParentNotFound
		}
	}
	return nil
}
