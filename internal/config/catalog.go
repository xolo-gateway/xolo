package config

import "time"

// CatalogConfig points at the open model catalog (github.com/xolo-gateway/catalog)
// the provider pages import from and the model form pre-fills from. Xolo
// embeds no copy of it: it reads the release at URL, and the import and
// pre-fill are offered only while the catalog is enabled and has a URL.
type CatalogConfig struct {
	Enabled         bool          `env:"ENABLED" envDefault:"true"`
	URL             string        `env:"URL" envDefault:"https://github.com/xolo-gateway/catalog/releases/latest/download/catalog.json"`
	RefreshInterval time.Duration `env:"REFRESH_INTERVAL" envDefault:"24h"`
}
