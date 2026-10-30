package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// CatalogOriginStore keeps the catalog entry each LLM model was imported
// from. An origin is not part of the model's configuration: saving a model
// keeps it, and deleting the model deletes it.
type CatalogOriginStore interface {
	// GetCatalogOrigin returns the origin of the model id, nil when it has
	// none. It returns ErrNotFound when the model does not exist.
	GetCatalogOrigin(ctx context.Context, id model.LLMModelID) (*model.CatalogOrigin, error)
	// ListCatalogTrackedModels lists the models with an origin, in every
	// organization.
	ListCatalogTrackedModels(ctx context.Context) ([]model.CatalogTrackedModel, error)
	// SaveCatalogOrigin records the origin of the model id, or forgets it
	// when origin is nil. It returns ErrNotFound when the model does not
	// exist.
	SaveCatalogOrigin(ctx context.Context, id model.LLMModelID, origin *model.CatalogOrigin) error
	// UpdateCatalogOrigin replaces the origin of the model id, as following
	// the catalog does: it returns ErrNotFound, writing nothing, when the
	// model does not exist or has no origin any more.
	UpdateCatalogOrigin(ctx context.Context, id model.LLMModelID, origin model.CatalogOrigin) error
}
