// Package genai holds what Xolo shares in building genai provider clients.
package genai

import (
	"maps"
	"reflect"

	"github.com/bornholm/genai/llm/provider"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// SetCommonOptions fills the provider.CommonOptions embedded in opts, the
// options of a provider (a pointer to its Options struct): its endpoint, key
// and model, and the headers the provider configures (see
// model.ProviderRequestHeaders). The headers are copied, never shared with
// the stored provider.
//
// Options without CommonOptions are left untouched, on purpose: a plugin
// provider's (plugin.Options) carry none, as it reads its settings from its
// own environment. Failing there would break every plugin provider.
func SetCommonOptions(opts any, baseURL, apiKey, modelName string, headers *model.ProviderRequestHeaders) {
	field := reflect.ValueOf(opts).Elem().FieldByName("CommonOptions")
	if !field.IsValid() {
		return
	}
	common, ok := field.Addr().Interface().(*provider.CommonOptions)
	if !ok {
		return
	}
	common.BaseURL = baseURL
	common.APIKey = apiKey
	common.Model = modelName
	if headers != nil {
		common.Headers = maps.Clone(headers.Headers)
		common.SessionHeader = headers.SessionHeader
	}
}
