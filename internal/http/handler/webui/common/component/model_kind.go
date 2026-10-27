package component

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// ModelKindLabel names a model kind in the interface.
func ModelKindLabel(ctx context.Context, k model.ModelKind) string {
	switch k {
	case model.ModelKindDecision:
		return i18n.T(ctx, "common.model_kind.decision")
	case model.ModelKindEmbeddings:
		return i18n.T(ctx, "common.model_kind.embeddings")
	default:
		return i18n.T(ctx, "common.model_kind.completion")
	}
}
