package gorm

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateProvider implements port.ProviderStore.
func (s *Store) CreateProvider(ctx context.Context, p model.Provider) error {
	return s.recorded(ctx, tracking("provider", string(p.ID())), func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Create(fromProvider(p)).Error)
	})
}

// GetProviderByID implements port.ProviderStore.
func (s *Store) GetProviderByID(ctx context.Context, id model.ProviderID) (model.Provider, error) {
	var p Provider
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&p, "id = ?", string(id)).Error; err != nil {
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
	return &wrappedProvider{&p}, nil
}

// ListProviders implements port.ProviderStore.
func (s *Store) ListProviders(ctx context.Context, orgID model.OrgID) ([]model.Provider, error) {
	var providers []*Provider
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("org_id = ?", string(orgID)).Order("name ASC").Find(&providers).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.Provider, 0, len(providers))
	for _, p := range providers {
		result = append(result, &wrappedProvider{p})
	}
	return result, nil
}

// SaveProvider implements port.ProviderStore.
func (s *Store) SaveProvider(ctx context.Context, p model.Provider) error {
	return s.recorded(ctx, tracking("provider", string(p.ID())), func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Create(fromProvider(p)).Error)
	})
}

// DeleteProvider implements port.ProviderStore.
func (s *Store) DeleteProvider(ctx context.Context, id model.ProviderID) error {
	return s.recorded(ctx, tracking("provider", string(id)), func(ctx context.Context, db *gorm.DB) error {
		result := db.Delete(&Provider{}, "id = ?", string(id))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		return nil
	})
}

// CreateLLMModel implements port.ProviderStore.
func (s *Store) CreateLLMModel(ctx context.Context, m model.LLMModel) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Create(fromLLMModel(m)).Error)
	})
}

// GetLLMModelByID implements port.ProviderStore.
func (s *Store) GetLLMModelByID(ctx context.Context, id model.LLMModelID) (model.LLMModel, error) {
	var m LLMModel
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&m, "id = ?", string(id)).Error; err != nil {
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
	return &wrappedLLMModel{&m}, nil
}

// GetLLMModelByProxyName implements port.ProviderStore.
func (s *Store) GetLLMModelByProxyName(ctx context.Context, orgID model.OrgID, proxyName string) (model.LLMModel, error) {
	var m LLMModel
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		// LLMModel.Enabled maps to an integer column, so the flag is bound as
		// 1: PostgreSQL refuses to encode a Go bool into a bigint parameter.
		if err := db.Where("org_id = ? AND proxy_name = ? AND enabled = ?", string(orgID), proxyName, 1).First(&m).Error; err != nil {
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
	return &wrappedLLMModel{&m}, nil
}

// ListLLMModels implements port.ProviderStore.
func (s *Store) ListLLMModels(ctx context.Context, orgID model.OrgID) ([]model.LLMModel, error) {
	var models []*LLMModel
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("org_id = ?", string(orgID)).Order("proxy_name ASC").Find(&models).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.LLMModel, 0, len(models))
	for _, m := range models {
		result = append(result, &wrappedLLMModel{m})
	}
	return result, nil
}

// ListEnabledLLMModels implements port.ProviderStore.
func (s *Store) ListEnabledLLMModels(ctx context.Context, orgID model.OrgID) ([]model.LLMModel, error) {
	var models []*LLMModel
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("org_id = ? AND enabled = ?", string(orgID), 1).Order("proxy_name ASC").Find(&models).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.LLMModel, 0, len(models))
	for _, m := range models {
		result = append(result, &wrappedLLMModel{m})
	}
	return result, nil
}

// SaveLLMModel implements port.ProviderStore.
func (s *Store) SaveLLMModel(ctx context.Context, m model.LLMModel) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Create(fromLLMModel(m)).Error)
	})
}

// DeleteLLMModel implements port.ProviderStore.
func (s *Store) DeleteLLMModel(ctx context.Context, id model.LLMModelID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result := db.Delete(&LLMModel{}, "id = ?", string(id))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		return nil
	})
}

var _ port.ProviderStore = &Store{}
