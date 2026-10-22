package component

import "github.com/xolo-gateway/xolo/internal/core/model"

// ModelKindLabel names a model kind in the interface.
func ModelKindLabel(k model.ModelKind) string {
	switch k {
	case model.ModelKindDecision:
		return "Décision"
	case model.ModelKindEmbeddings:
		return "Embeddings"
	default:
		return "Complétion"
	}
}
