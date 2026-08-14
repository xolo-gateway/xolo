package gorm

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateTenant implements port.TenantStore.
func (s *Store) CreateTenant(ctx context.Context, tenant model.Tenant) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Create(fromTenant(tenant)).Error; err != nil {
			if isUniqueViolation(err, "tenants", "slug") {
				return errors.Wrapf(port.ErrAlreadyExists, "slug %q is already used by another tenant", tenant.Slug())
			}
			return errors.WithStack(err)
		}
		return nil
	})
}

// GetTenantByID implements port.TenantStore.
func (s *Store) GetTenantByID(ctx context.Context, id model.TenantID) (model.Tenant, error) {
	var tenant Tenant
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&tenant, "id = ?", string(id)).Error; err != nil {
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
	return &wrappedTenant{&tenant}, nil
}

// GetTenantBySlug implements port.TenantStore.
func (s *Store) GetTenantBySlug(ctx context.Context, slug string) (model.Tenant, error) {
	var tenant Tenant
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&tenant, "slug = ?", slug).Error; err != nil {
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
	return &wrappedTenant{&tenant}, nil
}

// ListTenants implements port.TenantStore.
func (s *Store) ListTenants(ctx context.Context, opts port.ListTenantsOptions) ([]model.Tenant, int64, error) {
	var tenants []*Tenant
	var total int64

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&Tenant{})

		if err := query.Count(&total).Error; err != nil {
			return errors.WithStack(err)
		}

		if opts.Limit != nil {
			query = query.Limit(*opts.Limit)
		}
		if opts.Page != nil && opts.Limit != nil {
			query = query.Offset(*opts.Page * *opts.Limit)
		}

		return errors.WithStack(query.Order("name ASC").Find(&tenants).Error)
	})
	if err != nil {
		return nil, 0, err
	}

	result := make([]model.Tenant, 0, len(tenants))
	for _, t := range tenants {
		result = append(result, &wrappedTenant{t})
	}
	return result, total, nil
}

// SaveTenant implements port.TenantStore.
func (s *Store) SaveTenant(ctx context.Context, tenant model.Tenant) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Create(fromTenant(tenant)).Error)
	})
}

// DeleteTenant implements port.TenantStore. It replays the organization cascade
// for every organization the tenant owns, then removes the tenant users and the
// rows keyed on them: users are tenant-scoped, so nothing outside this tenant
// can reference them.
func (s *Store) DeleteTenant(ctx context.Context, id model.TenantID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		var exists Tenant
		if err := db.Select("id").First(&exists, "id = ?", string(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}

		var orgIDs []string
		if err := db.Model(&Organization{}).Where("tenant_id = ?", string(id)).Pluck("id", &orgIDs).Error; err != nil {
			return errors.WithStack(err)
		}
		for _, orgID := range orgIDs {
			if err := deleteOrgWithin(db, model.OrgID(orgID)); err != nil {
				return err
			}
		}

		// Materialized rather than kept as a subquery: the users are deleted
		// below, which would empty the subquery before the statements that
		// depend on it have run.
		var userIDs []string
		if err := db.Model(&User{}).Where("tenant_id = ?", string(id)).Pluck("id", &userIDs).Error; err != nil {
			return errors.WithStack(err)
		}

		if len(userIDs) > 0 {
			userScoped := []any{
				&UserRole{},
				&UserPreferences{},
				&PersonalVirtualModel{},
			}
			for _, m := range userScoped {
				if err := db.Where("user_id IN ?", userIDs).Delete(m).Error; err != nil {
					return errors.WithStack(err)
				}
			}

			if err := db.Where("owner_id IN ?", userIDs).Delete(&AuthToken{}).Error; err != nil {
				return errors.WithStack(err)
			}
			if err := db.Where("scope = ? AND scope_id IN ?", string(model.QuotaScopeUser), userIDs).Delete(&Quota{}).Error; err != nil {
				return errors.WithStack(err)
			}
			if err := db.Where("id IN ?", userIDs).Delete(&User{}).Error; err != nil {
				return errors.WithStack(err)
			}
		}

		return errors.WithStack(db.Delete(&Tenant{}, "id = ?", string(id)).Error)
	})
}
