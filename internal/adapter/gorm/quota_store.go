package gorm

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SetQuota implements port.QuotaStore.
// The upsert keeps the identifier of the quota already set on the scope: that
// row is the one recorded, or the new one when the scope has none.
func (s *Store) SetQuota(ctx context.Context, quota model.Quota) error {
	track := func(r *mutationRecorder) error {
		var ids []string
		if err := r.db.Table("quota").Where("scope = ? AND scope_id = ?", string(quota.Scope()), quota.ScopeID()).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return r.track("quota", string(quota.ID()))
		}
		return r.trackQuota(quota.Scope(), quota.ScopeID())
	}
	return s.recorded(ctx, track, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "scope"}, {Name: "scope_id"}},
			UpdateAll: true,
		}).Create(fromQuota(quota)).Error)
	})
}

// DeleteQuota implements port.ProvisioningBusinessStore. The spend recorded
// on its scope is kept: a quota set again later starts from it.
func (s *Store) DeleteQuota(ctx context.Context, id model.QuotaID) error {
	return s.recorded(ctx, tracking("quota", string(id)), func(ctx context.Context, db *gorm.DB) error {
		result := db.Delete(&Quota{}, "id = ?", string(id))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		return nil
	})
}

// GetQuotaByID implements port.ProvisioningBusinessStore.
func (s *Store) GetQuotaByID(ctx context.Context, id model.QuotaID) (model.Quota, error) {
	var q Quota
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&q, "id = ?", string(id)).Error; err != nil {
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
	return &wrappedQuota{&q}, nil
}

// GetQuota implements port.QuotaStore.
func (s *Store) GetQuota(ctx context.Context, scope model.QuotaScope, scopeID string) (model.Quota, error) {
	var q Quota
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Where("scope = ? AND scope_id = ?", string(scope), scopeID).First(&q).Error; err != nil {
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
	return &wrappedQuota{&q}, nil
}

// ListQuotas implements port.QuotaStore.
func (s *Store) ListQuotas(ctx context.Context, scope model.QuotaScope, scopeIDs []string) ([]model.Quota, error) {
	if len(scopeIDs) == 0 {
		return nil, nil
	}
	var rows []*Quota
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("scope = ? AND scope_id IN ?", string(scope), scopeIDs).Order("scope_id").Find(&rows).Error)
	})
	if err != nil {
		return nil, err
	}
	quotas := make([]model.Quota, 0, len(rows))
	for _, q := range rows {
		quotas = append(quotas, &wrappedQuota{q})
	}
	return quotas, nil
}

var _ port.QuotaStore = &Store{}
