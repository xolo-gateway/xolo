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
func (s *Store) createTenant(ctx context.Context, tenant model.Tenant) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		if _, err := model.ParseTenantID(string(tenant.ID())); err != nil {
			return port.ErrInvalid
		}

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
func (s *Store) saveTenant(ctx context.Context, tenant model.Tenant) error {
	if _, err := model.ParseTenantID(string(tenant.ID())); err != nil {
		return port.ErrInvalid
	}
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Create(fromTenant(tenant)).Error
		if isUniqueViolation(err, "tenants", "slug") {
			return port.ErrAlreadyExists
		}
		return errors.WithStack(err)
	})
}

func (s *Store) CreateTenant(ctx context.Context, tenant model.Tenant) error {
	return s.mutate(ctx, "tenant", string(tenant.ID()), func(bound *Store) error { return bound.createTenant(ctx, tenant) })
}

func (s *Store) SaveTenant(ctx context.Context, tenant model.Tenant) error {
	return s.mutate(ctx, "tenant", string(tenant.ID()), func(bound *Store) error { return bound.saveTenant(ctx, tenant) })
}

// DeleteTenant schedules a frozen exportable scope; physical cleanup requires a receipt.
func (s *Store) DeleteTenant(ctx context.Context, id model.TenantID) error {
	if _, err := s.GetTenantByID(ctx, id); err != nil {
		return err
	}
	return s.localDeletion(ctx, model.CommonScope{Family: "tenant"}, string(id))
}
