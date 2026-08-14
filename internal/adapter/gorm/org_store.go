package gorm

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateOrg implements port.OrgStore.
func (s *Store) CreateOrg(ctx context.Context, org model.Organization) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Create(fromOrganization(org)).Error; err != nil {
			if isUniqueViolation(err, "organizations", "slug") {
				return errors.Wrapf(port.ErrAlreadyExists, "slug %q is already used by another organization", org.Slug())
			}
			return errors.WithStack(err)
		}
		return nil
	})
}

// GetOrgByID implements port.OrgStore.
func (s *Store) GetOrgByID(ctx context.Context, id model.OrgID) (model.Organization, error) {
	var org Organization
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&org, "id = ?", string(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &wrappedOrganization{&org}, nil
}

// GetOrgBySlug implements port.OrgStore.
func (s *Store) GetOrgBySlug(ctx context.Context, tenantID model.TenantID, slug string) (model.Organization, error) {
	var org Organization
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&org, "tenant_id = ? AND slug = ?", string(tenantID), slug).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &wrappedOrganization{&org}, nil
}

// ListOrgs implements port.OrgStore.
func (s *Store) ListOrgs(ctx context.Context, opts port.ListOrgsOptions) ([]model.Organization, int64, error) {
	var orgs []*Organization
	var total int64

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&Organization{})

		if opts.TenantID != nil {
			query = query.Where("tenant_id = ?", string(*opts.TenantID))
		}

		if err := query.Count(&total).Error; err != nil {
			return errors.WithStack(err)
		}

		if opts.Limit != nil {
			query = query.Limit(*opts.Limit)
		}
		if opts.Page != nil && opts.Limit != nil {
			query = query.Offset(*opts.Page * *opts.Limit)
		}

		return errors.WithStack(query.Order("name ASC").Find(&orgs).Error)
	})
	if err != nil {
		return nil, 0, err
	}

	result := make([]model.Organization, 0, len(orgs))
	for _, o := range orgs {
		result = append(result, &wrappedOrganization{o})
	}
	return result, total, nil
}

// SaveOrg implements port.OrgStore.
func (s *Store) SaveOrg(ctx context.Context, org model.Organization) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Create(fromOrganization(org)).Error)
	})
}

// DeleteOrg implements port.OrgStore. It removes every row scoped to the
// organization before deleting it: the tables that declare a foreign key on
// `organizations` have no database-level cascade, and the remaining org-scoped
// tables would otherwise be orphaned — in particular the applications and their
// auth tokens, which stay resolvable by FindAuthToken as long as they exist.
func (s *Store) DeleteOrg(ctx context.Context, id model.OrgID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		var exists Organization
		if err := db.Select("id").First(&exists, "id = ?", string(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}

		return deleteOrgWithin(db, id)
	})
}

// deleteOrgWithin holds the actual cascade, without the existence check, so
// DeleteTenant can replay it for every organization it owns inside its own
// transaction. The list of org-scoped models below is the de facto definition
// of the organization footprint: any new org-scoped table must be added here.
func deleteOrgWithin(db *gorm.DB, id model.OrgID) error {
	// Subqueries are evaluated when the DELETE they belong to runs, so every
	// statement using them must be issued before its parent rows are gone.
	membershipIDs := db.Model(&Membership{}).Select("id").Where("org_id = ?", string(id))
	roleIDs := db.Model(&Role{}).Select("id").Where("org_id = ?", string(id))
	applicationIDs := db.Model(&Application{}).Select("id").Where("org_id = ?", string(id))
	alertIDs := db.Model(&Alert{}).Select("id").Where("org_id = ?", string(id))

	if err := db.Where("membership_id IN (?)", membershipIDs).Delete(&MembershipRole{}).Error; err != nil {
		return errors.WithStack(err)
	}

	if err := db.Where("role_id IN (?)", roleIDs).Delete(&MembershipRole{}).Error; err != nil {
		return errors.WithStack(err)
	}
	if err := db.Where("role_id IN (?)", roleIDs).Delete(&ApplicationRole{}).Error; err != nil {
		return errors.WithStack(err)
	}
	if err := db.Where("role_id IN (?)", roleIDs).Delete(&RolePermission{}).Error; err != nil {
		return errors.WithStack(err)
	}
	if err := db.Where("role_id IN (?)", roleIDs).Delete(&RoleModel{}).Error; err != nil {
		return errors.WithStack(err)
	}

	if err := db.Where("application_id IN (?)", applicationIDs).Delete(&ApplicationRole{}).Error; err != nil {
		return errors.WithStack(err)
	}
	if err := db.Where("org_id = ? OR application_id IN (?)", string(id), applicationIDs).Delete(&AuthToken{}).Error; err != nil {
		return errors.WithStack(err)
	}
	if err := db.Where("scope = ? AND scope_id IN (?)", string(model.QuotaScopeApplication), applicationIDs).Delete(&Quota{}).Error; err != nil {
		return errors.WithStack(err)
	}
	if err := db.Where("alert_id IN (?)", alertIDs).Delete(&AlertIncident{}).Error; err != nil {
		return errors.WithStack(err)
	}

	if err := db.Where("scope = ? AND scope_id = ?", string(model.QuotaScopeOrg), string(id)).Delete(&Quota{}).Error; err != nil {
		return errors.WithStack(err)
	}

	orgScoped := []any{
		&Application{},
		&Membership{},
		&InviteToken{},
		&Role{},
		&Alert{},
		&AlertIncident{},
		&UsageRecord{},
		&Event{},
		&EventSettings{},
		&VirtualModel{},
		&Middleware{},
		&PluginNodeSecret{},
		&Provider{},
		&LLMModel{},
	}
	for _, m := range orgScoped {
		if err := db.Where("org_id = ?", string(id)).Delete(m).Error; err != nil {
			return errors.WithStack(err)
		}
	}

	return errors.WithStack(db.Delete(&Organization{}, "id = ?", string(id)).Error)
}

// AddMember implements port.OrgStore.
func (s *Store) AddMember(ctx context.Context, membership model.Membership) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Create(fromMembership(membership)).Error)
	})
}

// RemoveMember implements port.OrgStore. The membership_roles rows are deleted
// first: the join table references the membership and has no database-level
// cascade, so removing a member holding any role would otherwise fail.
func (s *Store) RemoveMember(ctx context.Context, id model.MembershipID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Where("membership_id = ?", string(id)).Delete(&MembershipRole{}).Error; err != nil {
			return errors.WithStack(err)
		}

		result := db.Delete(&Membership{}, "id = ?", string(id))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		return nil
	})
}

// GetMembership implements port.OrgStore.
func (s *Store) GetMembership(ctx context.Context, id model.MembershipID) (model.Membership, error) {
	var m Membership
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Preload("User").Preload("Org").Preload("Roles").First(&m, "id = ?", string(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &wrappedMembership{&m}, nil
}

// GetUserOrgMembership implements port.OrgStore.
func (s *Store) GetUserOrgMembership(ctx context.Context, userID model.UserID, orgID model.OrgID) (model.Membership, error) {
	var m Membership
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Preload("User").Preload("Org").Preload("Roles").
			Where("user_id = ? AND org_id = ?", string(userID), string(orgID)).
			First(&m).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &wrappedMembership{&m}, nil
}

// ListOrgMembers implements port.OrgStore.
func (s *Store) ListOrgMembers(ctx context.Context, orgID model.OrgID, opts port.ListOrgMembersOptions) ([]model.Membership, int64, error) {
	var members []*Membership
	var total int64

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		base := db.Model(&Membership{}).Where("org_id = ?", string(orgID))

		if err := base.Count(&total).Error; err != nil {
			return errors.WithStack(err)
		}

		query := db.Preload("User").Preload("Org").Preload("Roles").Where("org_id = ?", string(orgID))

		if opts.Page != nil && opts.Limit != nil {
			query = query.Offset(*opts.Page * *opts.Limit)
		}
		if opts.Limit != nil {
			query = query.Limit(*opts.Limit)
		}

		return errors.WithStack(query.Find(&members).Error)
	})
	if err != nil {
		return nil, 0, err
	}

	result := make([]model.Membership, 0, len(members))
	for _, m := range members {
		result = append(result, &wrappedMembership{m})
	}
	return result, total, nil
}

// GetUserMemberships implements port.OrgStore.
func (s *Store) GetUserMemberships(ctx context.Context, userID model.UserID) ([]model.Membership, error) {
	var members []*Membership
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Preload("Org").Preload("Roles").Preload("Roles.Permissions").
			Where("user_id = ?", string(userID)).
			Find(&members).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.Membership, 0, len(members))
	for _, m := range members {
		result = append(result, &wrappedMembership{m})
	}
	return result, nil
}

// IsMember implements port.OrgStore.
func (s *Store) IsMember(ctx context.Context, userID model.UserID, orgID model.OrgID) (bool, error) {
	var count int64
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&Membership{}).
			Where("user_id = ? AND org_id = ?", string(userID), string(orgID)).
			Count(&count).Error)
	})
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

var _ port.OrgStore = &Store{}
