package proxy

// This file must not import any genai provider package: it checks that the
// production code of this package (the blank imports in org_model_router.go)
// registers every type the provider form offers.

import (
	"testing"

	"github.com/bornholm/genai/llm/provider"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
)

func TestRouterRegistersEveryFormProviderType(t *testing.T) {
	for _, providerType := range component.ProviderTypes {
		if provider.NewChatCompletionProviderOptions(provider.Name(providerType)) == nil {
			t.Errorf("provider type %q is offered by the form but not registered by the proxy router imports", providerType)
		}
	}
}
