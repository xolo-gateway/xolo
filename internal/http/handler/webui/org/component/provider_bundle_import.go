package component

import (
	"context"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// providerBundleFieldKeys names each field of a bundle import dry run.
var providerBundleFieldKeys = map[string]string{
	service.BundleFieldType:           "org.provider_bundle.field.type",
	service.BundleFieldBaseURL:        "org.provider_bundle.field.base_url",
	service.BundleFieldCurrency:       "org.provider_bundle.field.currency",
	service.BundleFieldCloudTier:      "org.provider_bundle.field.cloud_tier",
	service.BundleFieldRetry:          "org.provider_bundle.field.retry",
	service.BundleFieldRateLimit:      "org.provider_bundle.field.rate_limit",
	service.BundleFieldRequestHeaders: "org.provider_bundle.field.request_headers",
	service.BundleFieldBilling:        "org.provider_bundle.field.billing",
	service.BundleFieldProxyName:      "org.provider_bundle.field.proxy_name",
	service.BundleFieldDescription:    "org.provider_bundle.field.description",
	service.BundleFieldKind:           "org.provider_bundle.field.kind",
	service.BundleFieldEnabled:        "org.provider_bundle.field.enabled",
	service.BundleFieldPrices:         "org.provider_bundle.field.prices",
	service.BundleFieldWindows:        "org.provider_bundle.field.windows",
	service.BundleFieldActiveParams:   "org.provider_bundle.field.active_params",
	service.BundleFieldThroughput:     "org.provider_bundle.field.throughput",
	service.BundleFieldCapabilities:   "org.provider_bundle.field.capabilities",
	service.BundleFieldAPI:            "org.provider_bundle.field.api",
	service.BundleFieldTokenLimit:     "org.provider_bundle.field.token_limit",
	service.BundleFieldExtraBody:      "org.provider_bundle.field.extra_body",
}

// ProviderBundleFieldLabel names a field of a bundle import in the interface.
func ProviderBundleFieldLabel(ctx context.Context, field string) string {
	return providerBundleFieldLabel(ctx, field)
}

func providerBundleFieldLabel(ctx context.Context, field string) string {
	if key, ok := providerBundleFieldKeys[field]; ok {
		return i18n.T(ctx, key)
	}
	return field
}

// providerBundleKeyOverrides lists the adjustments of keys an import removes.
func providerBundleKeyOverrides(ctx context.Context, refs []model.KeyOverrideRef) string {
	items := make([]string, len(refs))
	for i, r := range refs {
		items[i] = i18n.T(ctx, "org.provider_bundle.key_override_ref", r.Key, r.Window)
	}
	return strings.Join(items, "; ")
}
