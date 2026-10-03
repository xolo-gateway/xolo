package gorm

import (
	"context"
	"errors"
	"fmt"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ReservedDomain struct {
	Hostname string `gorm:"primaryKey"`
}

type Domain struct {
	Hostname string `gorm:"primaryKey"`
	TenantID string `gorm:"index;not null"`
	Status   string `gorm:"not null"`
}

func (s *Store) SaveDomain(ctx context.Context, d model.Domain) error {
	hostname, err := model.NormalizeHostname(d.Hostname)
	if err != nil || !d.Status.Valid() {
		return port.ErrInvalid
	}
	d.Hostname = hostname
	return s.mutate(ctx, "domain", hostname, func(bound *Store) error {
		db, err := bound.getDatabase(ctx)
		if err != nil {
			return err
		}
		if _, err := bound.GetTenantByID(ctx, d.TenantID); err != nil {
			return err
		}
		var reserved int64
		if err := db.Model(&ReservedDomain{}).Where("hostname = ?", hostname).Count(&reserved).Error; err != nil {
			return err
		}
		if reserved > 0 {
			return port.ErrInvalidHostname
		}
		var old Domain
		err = db.First(&old, "hostname = ?", hostname).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && old.TenantID != string(d.TenantID) {
			return port.ErrAlreadyExists
		}
		return db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "hostname"}}, DoUpdates: clause.AssignmentColumns([]string{"status"})}).Create(&Domain{Hostname: hostname, TenantID: string(d.TenantID), Status: string(d.Status)}).Error
	})
}
func (s *Store) GetDomain(ctx context.Context, hostname string) (model.Domain, error) {
	var d Domain
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error { return db.First(&d, "hostname = ?", hostname).Error })
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = port.ErrNotFound
	}
	return model.Domain{Hostname: d.Hostname, TenantID: model.TenantID(d.TenantID), Status: model.Status(d.Status)}, err
}
func (s *Store) ListDomains(ctx context.Context, id model.TenantID) ([]model.Domain, error) {
	var rows []Domain
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return db.Where("tenant_id = ?", string(id)).Order("hostname").Find(&rows).Error
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.Domain, 0, len(rows))
	for _, d := range rows {
		result = append(result, model.Domain{Hostname: d.Hostname, TenantID: id, Status: model.Status(d.Status)})
	}
	return result, nil
}

// SetCommonMembership projects the common role onto builtin permissions while
// retaining every custom assignment. Membership identity is the org/user pair.
func (s *Store) SetCommonMembership(ctx context.Context, orgID model.OrgID, userID model.UserID, role model.MembershipRole, status model.Status) error {
	if !role.Valid() || !status.Valid() {
		return port.ErrInvalid
	}
	return s.identityTransaction(ctx, func(bound *Store) error {
		org, err := bound.GetOrgByID(ctx, orgID)
		if err != nil {
			return err
		}
		user, err := bound.GetUserByID(ctx, userID)
		if err != nil {
			return err
		}
		if user.TenantID() != org.TenantID() {
			return port.ErrNotFound
		}
		member, err := bound.GetUserOrgMembership(ctx, userID, orgID)
		if errors.Is(err, port.ErrNotFound) {
			m := model.NewMembership(userID, orgID)
			if err := bound.AddMember(ctx, m); err != nil {
				return err
			}
			member = m
		} else if err != nil {
			return err
		}
		if err := bound.track(ctx, "membership", string(member.ID())); err != nil {
			return err
		}
		if err := bound.EnsureBuiltinRoles(ctx, orgID); err != nil {
			return err
		}
		roles, err := bound.ListOrgRoles(ctx, orgID)
		if err != nil {
			return err
		}
		var ids []model.RoleID
		for _, r := range member.Roles() {
			if !r.Builtin() {
				ids = append(ids, r.ID())
			}
		}
		for _, r := range roles {
			if r.BuiltinKind() == string(role) {
				ids = append(ids, r.ID())
			}
		}
		if err := bound.SetMembershipRoles(ctx, member.ID(), ids); err != nil {
			return err
		}
		db, _ := bound.getDatabase(ctx)
		return db.Model(&Membership{}).Where("id = ?", string(member.ID())).Updates(map[string]any{"common_role": string(role), "status": string(status)}).Error
	})
}
func validateMemberParents(db *gorm.DB, m model.Membership) error {
	var org Organization
	var user User
	if err := db.First(&org, "id = ?", string(m.OrgID())).Error; err != nil {
		return invitationReadError(err)
	}
	if err := db.First(&user, "id = ?", string(m.UserID())).Error; err != nil {
		return invitationReadError(err)
	}
	if org.TenantID != user.TenantID {
		return port.ErrNotFound
	}
	if !m.CommonRole().Valid() || !m.Status().Valid() {
		return fmt.Errorf("%w: membership role/status", port.ErrInvalid)
	}
	return nil
}

func (s *Store) ReserveDomain(ctx context.Context, hostname string) error {
	h, err := model.NormalizeHostname(hostname)
	if err != nil {
		return err
	}
	return s.identityTransaction(ctx, func(bound *Store) error {
		db, err := bound.getDatabase(ctx)
		if err != nil {
			return err
		}
		var count int64
		if err := db.Model(&Domain{}).Where("hostname = ?", h).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return port.ErrAlreadyExists
		}
		return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&ReservedDomain{Hostname: h}).Error
	})
}
