package setup

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/catalog"
	"github.com/xolo-gateway/xolo/internal/config"
)

var getCatalogSourceFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*catalog.Source, error) {
	return catalog.NewSource(conf.Catalog.URL, conf.Catalog.RefreshInterval), nil
})
