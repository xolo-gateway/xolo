package component

import (
	"context"
	"encoding/json"

	"github.com/xolo-gateway/xolo/internal/catalog"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// catalogProvidersSuggestedFirst lists the providers of c, the suggested one
// first.
func catalogProvidersSuggestedFirst(c *catalog.Catalog, suggested string) []catalog.Provider {
	providers := make([]catalog.Provider, 0, len(c.Providers))
	for _, p := range c.Providers {
		if p.ID == suggested {
			providers = append([]catalog.Provider{p}, providers...)
			continue
		}
		providers = append(providers, p)
	}
	return providers
}

// catalogFieldLabels names, for the pre-fill's alert, the model form fields
// the catalog lookup reports as estimated.
func catalogFieldLabels(ctx context.Context, currency string) string {
	labels := map[string]string{
		"context_window":      i18n.T(ctx, "common.catalog.context_window"),
		"output_window":       i18n.T(ctx, "common.catalog.max_output"),
		"active_params":       i18n.T(ctx, "org.model.active_params"),
		"prompt_cost":         i18n.T(ctx, "org.model.prompt_cost", currency),
		"cached_prompt_cost":  i18n.T(ctx, "org.model.cached_prompt_cost", currency),
		"completion_cost":     i18n.T(ctx, "org.model.completion_cost", currency),
		"tokens_per_sec_low":  i18n.T(ctx, "org.model.throughput_min"),
		"tokens_per_sec_high": i18n.T(ctx, "org.model.throughput_max"),
	}
	data, err := json.Marshal(labels)
	if err != nil {
		return "{}"
	}
	return string(data)
}
