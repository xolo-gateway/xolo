package proxy

// This file must not import any genai provider package: it checks that the
// production code of this package (the blank imports in org_model_router.go
// and decision.go) registers every type the provider form offers.

import (
	"testing"

	"github.com/bornholm/genai/llm/provider"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TestRouterRegistersEveryFormProviderType checks each type against the
// kinds model.ProviderTypeKinds says it serves, both ways: a kind it is said
// to serve must have a client, and a client of a kind must be declared, or
// the model form would refuse a model the type can serve.
func TestRouterRegistersEveryFormProviderType(t *testing.T) {
	clients := map[model.ModelKind]func(provider.Name) any{
		model.ModelKindCompletion: provider.NewChatCompletionProviderOptions,
		model.ModelKindEmbeddings: provider.NewEmbeddingsProviderOptions,
		model.ModelKindDecision:   provider.NewDecisionProviderOptions,
	}
	for _, providerType := range model.ProviderTypes() {
		for kind, newOptions := range clients {
			registered := newOptions(provider.Name(providerType)) != nil
			declared := model.ProviderTypeServes(providerType, kind)
			if declared && !registered {
				t.Errorf("provider type %q serves %s models, but the proxy router imports register no client for them", providerType, kind)
			}
			if registered && !declared {
				t.Errorf("provider type %q has a %s client, but model.ProviderTypeKinds does not list the kind", providerType, kind)
			}
		}
	}
}
