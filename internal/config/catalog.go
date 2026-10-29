package config

import "time"

// CatalogConfig points at the open model catalog (github.com/xolo-gateway/catalog)
// the provider and model forms import from. An instance without network
// access keeps working from the snapshot embedded in the binary.
type CatalogConfig struct {
	URL             string        `env:"URL" envDefault:"https://github.com/xolo-gateway/catalog/releases/latest/download/catalog.json"`
	RefreshInterval time.Duration `env:"REFRESH_INTERVAL" envDefault:"24h"`
}
