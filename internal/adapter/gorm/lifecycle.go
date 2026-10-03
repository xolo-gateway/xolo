package gorm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ResourceDeletion struct {
	Family       string `gorm:"primaryKey"`
	ResourceID   string `gorm:"primaryKey"`
	TenantID     string `gorm:"index"`
	ETag         string `gorm:"column:etag"`
	DeletedAt    time.Time
	PurgeAfter   time.Time `gorm:"index"`
	ExportSHA256 string
	ConfirmedAt  *time.Time
	PurgedAt     *time.Time
	Attempts     int
	Diagnostic   string
}
type LifecycleControl struct {
	ID     int `gorm:"primaryKey;autoIncrement:false"`
	Bypass int
}
type DeletionArchive struct {
	Family     string `gorm:"primaryKey"`
	ResourceID string `gorm:"primaryKey"`
	ETag       string `gorm:"primaryKey;column:etag"`
	SHA256     string `gorm:"primaryKey"`
	CreatedAt  time.Time
}

func (d ResourceDeletion) view() model.Deletion {
	d.DeletedAt = d.DeletedAt.UTC()
	d.PurgeAfter = d.PurgeAfter.UTC()
	if d.ConfirmedAt != nil {
		v := d.ConfirmedAt.UTC()
		d.ConfirmedAt = &v
	}
	if d.PurgedAt != nil {
		v := d.PurgedAt.UTC()
		d.PurgedAt = &v
	}
	return model.Deletion{Family: d.Family, ResourceID: d.ResourceID, TenantID: d.TenantID, ETag: d.ETag, DeletedAt: d.DeletedAt, PurgeAfter: d.PurgeAfter, ExportSHA256: d.ExportSHA256, ConfirmedAt: d.ConfirmedAt, PurgedAt: d.PurgedAt, Attempts: d.Attempts, Diagnostic: d.Diagnostic}
}
func migrateLifecycle(db *gorm.DB) error {
	if err := db.AutoMigrate(&ResourceDeletion{}, &LifecycleControl{}, &DeletionArchive{}, &LeafVersion{}, &RetiredLogin{}); err != nil {
		return err
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&LifecycleControl{ID: 1}).Error; err != nil {
		return err
	}
	var leaves []CommonRecord
	if err := db.Where("family IN ?", []string{"tenant_domain", "organization_membership"}).Find(&leaves).Error; err != nil {
		return err
	}
	for _, r := range leaves {
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&LeafVersion{Family: r.Family, TenantID: r.TenantID, OrganizationID: r.OrganizationID, Key: r.Key, UpdatedAt: r.UpdatedAt}).Error; err != nil {
			return err
		}
	}
	return installLifecycleGuards(db)
}
func lifecycleError(err error) error {
	if err != nil && strings.Contains(err.Error(), "xolo_resource_deleted") {
		return port.ErrResourceDeleted
	}
	return err
}
func lifecycleScope(scope model.CommonScope, key string) (string, error) {
	if err := validateCommonScope(scope, key); err != nil {
		return "", err
	}
	if scope.Family != "tenant" && scope.Family != "organization" && scope.Family != "member" {
		return "", port.ErrInvalid
	}
	if key == "" {
		return "", port.ErrInvalid
	}
	if scope.Family == "tenant" {
		return key, nil
	}
	return scope.TenantID, nil
}
func deletionRow(db *gorm.DB, scope model.CommonScope, key string) (ResourceDeletion, error) {
	var d ResourceDeletion
	tid, err := lifecycleScope(scope, key)
	if err != nil {
		return d, err
	}
	err = db.Where("family = ? AND resource_id = ? AND tenant_id = ?", scope.Family, key, tid).First(&d).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = port.ErrNotFound
	}
	return d, err
}
func requireLive(db *gorm.DB, scope model.CommonScope, key string) error {
	tid := scope.TenantID
	if scope.Family == "tenant" {
		tid = key
	}
	q := db.Model(&ResourceDeletion{}).Where("(family = 'tenant' AND resource_id = ?) OR (family = ? AND resource_id = ? AND tenant_id = ?)", tid, scope.Family, key, tid)
	if scope.OrganizationID != "" {
		q = q.Or("family = 'organization' AND resource_id = ? AND tenant_id = ?", scope.OrganizationID, tid)
	}
	if scope.Family == "organization_membership" {
		q = q.Or("family = 'member' AND resource_id = ? AND tenant_id = ?", key, tid)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return err
	}
	if n != 0 {
		return port.ErrResourceDeleted
	}
	return nil
}
func (s *Store) ReadDeletion(ctx context.Context, scope model.CommonScope, key string) (model.Deletion, error) {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return model.Deletion{}, err
	}
	d, err := deletionRow(db.WithContext(ctx), scope, key)
	return d.view(), err
}
func (s *Store) ScheduleDeletion(ctx context.Context, scope model.CommonScope, key string, c model.MatchCondition, retention time.Duration) (model.Deletion, error) {
	var out model.Deletion
	if retention < time.Second || retention > 3650*24*time.Hour {
		return out, port.ErrInvalid
	}
	tid, err := lifecycleScope(scope, key)
	if err != nil {
		return out, err
	}
	if err = s.checkOwnership(ctx, scope.Family); err != nil {
		return out, err
	}
	err = s.identityTransaction(ctx, func(tx *Store) error {
		db, _ := tx.getDatabase(ctx)
		d, e := deletionRow(db, scope, key)
		if e == nil {
			if !c.Matches(d.ETag) {
				return port.ErrPreconditionFailed
			}
			out = d.view()
			return nil
		}
		if !errors.Is(e, port.ErrNotFound) {
			return e
		}
		item, e := readCommon(db, scope, key)
		if errors.Is(e, port.ErrNotFound) && c.Present {
			return port.ErrPreconditionFailed
		}
		if e != nil {
			return e
		}
		if !c.Matches(item.ETag) {
			return port.ErrPreconditionFailed
		}
		if e = requireLive(db, scope, key); e != nil {
			return e
		}
		if scope.Family == "member" {
			if e = checkDeletionOwners(db, tid, key); e != nil {
				return e
			}
		}
		now := db.NowFunc().UTC().Truncate(time.Microsecond)
		// Never reuse a version, including rapid consecutive state transitions.
		var rec CommonRecord
		if e = scopeQuery(db, scope).Where("key = ?", key).First(&rec).Error; e != nil {
			return e
		}
		if !now.After(rec.UpdatedAt) {
			now = rec.UpdatedAt.Add(time.Microsecond)
		}
		d = ResourceDeletion{Family: scope.Family, TenantID: tid, ResourceID: key, DeletedAt: now, PurgeAfter: now.Add(retention), ETag: model.CommonETag(now)}
		// Disable access before installing the freeze. Both changes commit together.
		value := any(0)
		if scope.Family == "member" {
			value = false
		}
		if e = db.Table(resourceTables[scope.Family]).Where("id = ?", key).Update("active", value).Error; e != nil {
			return e
		}
		if e = db.Create(&d).Error; e != nil {
			return e
		}
		if e = tx.checkDeletionOwnership(ctx, d); e != nil {
			return e
		}
		var rep map[string]any
		if e = json.Unmarshal([]byte(rec.Representation), &rep); e != nil {
			return e
		}
		rep["status"] = "deleted"
		raw, e := json.Marshal(rep)
		if e != nil {
			return e
		}
		rec.Representation = string(raw)
		rec.UpdatedAt = now
		if e = recordQuery(db.Model(&CommonRecord{}), &rec).Updates(map[string]any{"representation": rec.Representation, "updated_at": now}).Error; e != nil {
			return e
		}
		item, e = rec.item()
		if e != nil {
			return e
		}
		if e = tx.auditControlOperation(ctx, scope.Family, key, "deleted"); e != nil {
			return e
		}
		if e = publishExtension(model.WithActor(ctx, tx.mutations.actor), db, scope.Family, item, "deleted"); e != nil {
			return e
		}
		out = d.view()
		return nil
	})
	return out, err
}
func checkDeletionOwners(db *gorm.DB, tid, uid string) error {
	var u User
	if err := db.First(&u, "id = ? AND tenant_id = ?", uid, tid).Error; err != nil {
		return err
	}
	if u.Active && u.TenantRole == "owner" {
		var n int64
		if err := db.Model(&User{}).Where("tenant_id = ? AND id <> ? AND active = ? AND tenant_role = 'owner'", tid, uid, true).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return port.ErrLastOwner
		}
	}
	var memberships []Membership
	if err := db.Where("user_id = ? AND common_role = 'owner' AND status = 'active'", uid).Find(&memberships).Error; err != nil {
		return err
	}
	for _, m := range memberships {
		var frozen int64
		if err := db.Model(&ResourceDeletion{}).Where("family = 'organization' AND resource_id = ?", m.OrgID).Count(&frozen).Error; err != nil {
			return err
		}
		if frozen > 0 {
			continue
		}
		var n int64
		if err := db.Table("memberships m").Joins("JOIN users u ON u.id = m.user_id").Where("m.org_id = ? AND m.user_id <> ? AND m.common_role = 'owner' AND m.status = 'active' AND u.active = ?", m.OrgID, uid, true).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return port.ErrLastOwner
		}
	}
	return nil
}

// ConfigureLifecycle replaces legacy local cascades in the application composition.
// A disabled extension refuses deletion; existing frozen scopes stay protected.
func (s *Store) ConfigureLifecycle(enabled bool, retention time.Duration) {
	if !enabled {
		retention = 0
	}
	s.lifecycleRetention = &retention
}
func (s *Store) localDeletion(ctx context.Context, scope model.CommonScope, key string) error {
	if err := s.checkOwnership(ctx, scope.Family); err != nil {
		return err
	}
	if s.lifecycleRetention == nil || *s.lifecycleRetention == 0 {
		return port.ErrLifecycleDisabled
	}
	_, err := s.ScheduleDeletion(ctx, scope, key, model.MatchCondition{}, *s.lifecycleRetention)
	return err
}

// Composite deletion needs authority for every family it freezes, including
// children controlled by a different owner. Failure rolls back the whole freeze.
func (s *Store) checkDeletionOwnership(ctx context.Context, d ResourceDeletion) error {
	db, _ := s.getDatabase(ctx)
	families := map[string]string{"tenants": "tenant", "organizations": "organization", "users": "member", "memberships": "organization_membership", "domains": "tenant_domain", "roles": "role", "applications": "application", "quota": "quota", "alerts": "alert", "providers": "provider"}
	for _, t := range lifecycleTables(db) {
		f := families[t.table]
		if f == "" {
			continue
		}
		q := lifecycleQuery(db, t, d)
		if t.table == "roles" {
			q = q.Where("builtin = ?", false)
		}
		var n int64
		if err := q.Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			if err := s.checkOwnership(ctx, f); err != nil {
				return err
			}
		}
	}
	if d.Family == "tenant" {
		var n int64
		if err := db.Model(&WebhookSubscription{}).Where("tenant_id = ?", d.TenantID).Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return s.checkOwnership(ctx, "subscription")
		}
	}
	return nil
}
