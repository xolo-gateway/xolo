package gorm

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// GetCatalogOrigin implements port.CatalogOriginStore.
func (s *Store) GetCatalogOrigin(ctx context.Context, id model.LLMModelID) (*model.CatalogOrigin, error) {
	var m LLMModel
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Select("id", "catalog_origin").First(&m, "id = ?", string(id)).Error; err != nil {
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
	return m.CatalogOrigin.Val, nil
}

// ListCatalogTrackedModels implements port.CatalogOriginStore. The models of
// a frozen scope are left out: following the catalog writes none of them.
func (s *Store) ListCatalogTrackedModels(ctx context.Context) ([]model.CatalogTrackedModel, error) {
	var models []*LLMModel
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Select("id", "org_id", "provider_id", "catalog_origin").
			Where("catalog_origin IS NOT NULL").
			Where(notFrozen("llm_models", "llm_models")).
			Order("id ASC").Find(&models).Error)
	})
	if err != nil {
		return nil, err
	}
	tracked := make([]model.CatalogTrackedModel, 0, len(models))
	for _, m := range models {
		if m.CatalogOrigin.Val == nil {
			continue
		}
		tracked = append(tracked, model.CatalogTrackedModel{
			ModelID:    model.LLMModelID(m.ID),
			OrgID:      model.OrgID(m.OrgID),
			ProviderID: model.ProviderID(m.ProviderID),
			Origin:     *m.CatalogOrigin.Val,
		})
	}
	return tracked, nil
}

// SaveCatalogOrigin implements port.CatalogOriginStore. The origin is no
// part of the model's configuration, nor of its projection: it is written
// alone, like the runtime state of the other families.
func (s *Store) SaveCatalogOrigin(ctx context.Context, id model.LLMModelID, origin *model.CatalogOrigin) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result := db.Model(&LLMModel{}).Where("id = ?", string(id)).
			Update("catalog_origin", JSONColumn[model.CatalogOrigin]{Val: origin})
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		return nil
	})
}

var _ port.CatalogOriginStore = &Store{}
