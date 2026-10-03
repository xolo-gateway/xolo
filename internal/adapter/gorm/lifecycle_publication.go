package gorm

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

func publishExtension(ctx context.Context, db *gorm.DB, family string, item model.CommonItem, action string) error {
	if err := db.Exec("UPDATE publication_clocks SET sequence = sequence + 1 WHERE id = 1").Error; err != nil {
		return err
	}
	var clock PublicationClock
	if err := db.First(&clock, 1).Error; err != nil {
		return err
	}
	var feed CommonFeed
	if err := db.First(&feed, 1).Error; err != nil {
		return err
	}
	now := db.NowFunc().UTC().Truncate(time.Microsecond)
	ev := model.CommonEvent{SpecVersion: "1.0", ID: uuid.NewString(), Source: feed.Source, Type: family + "." + action + ".v1", Time: now, DataContentType: "application/json", Sequence: strconv.FormatInt(clock.Sequence, 10), RequestID: model.ActorFromContext(ctx).RequestID, Data: model.CommonEventData{ResourceType: family, Key: item.Key, ETag: item.ETag}}
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	id := item.Key.TenantID
	switch family {
	case "organization":
		id = item.Key.OrganizationID
	case "member", "organization_membership":
		id = item.Key.MemberID
	case "tenant_domain":
		id = item.Key.Hostname
	default:
		if item.Key.ResourceID != "" {
			id = item.Key.ResourceID
		}
	}
	return db.Create(&Publication{TenantID: item.Key.TenantID, OrgID: item.Key.OrganizationID, Resource: family, ResourceID: id, Sequence: clock.Sequence, CreatedAt: now, Payload: string(raw)}).Error
}
func (s *Store) DeleteCommonLeaf(ctx context.Context, scope model.CommonScope, key string, condition model.MatchCondition) error {
	if scope.Family != "tenant_domain" && scope.Family != "organization_membership" {
		return port.ErrInvalid
	}
	if err := validateCommonScope(scope, key); err != nil {
		return err
	}
	if key == "" {
		return port.ErrInvalid
	}
	if err := s.checkOwnership(ctx, scope.Family); err != nil {
		return err
	}
	return s.identityTransaction(ctx, func(tx *Store) error {
		db, _ := tx.getDatabase(ctx)
		if err := commonParents(db, scope); err != nil {
			return err
		}
		if err := requireLive(db, scope, key); err != nil {
			return err
		}
		if scope.Family == "organization_membership" {
			var n int64
			if err := db.Model(&User{}).Where("id = ? AND tenant_id = ?", key, scope.TenantID).Count(&n).Error; err != nil {
				return err
			}
			if n != 1 {
				return port.ErrParentNotFound
			}
		}
		old, err := readCommon(db, scope, key)
		if err == port.ErrNotFound {
			if condition.Present {
				return port.ErrPreconditionFailed
			}
			return nil
		}
		if err != nil {
			return err
		}
		if !condition.Matches(old.ETag) {
			return port.ErrPreconditionFailed
		}
		if scope.Family == "tenant_domain" {
			return tx.mutate(ctx, "domain", key, func(bound *Store) error {
				return db.Where("hostname = ? AND tenant_id = ?", key, scope.TenantID).Delete(&Domain{}).Error
			})
		}
		m, err := tx.GetUserOrgMembership(ctx, model.UserID(key), model.OrgID(scope.OrganizationID))
		if err != nil {
			return err
		}
		return tx.RemoveMember(ctx, m.ID())
	})
}
