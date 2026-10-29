package component

import "github.com/xolo-gateway/xolo/internal/catalog"

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
