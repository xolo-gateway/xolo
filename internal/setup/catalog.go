package setup

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/catalog"
	"github.com/xolo-gateway/xolo/internal/config"
)

// getCatalogSourceFromConfig returns the model catalog source, or nil when the
// catalog is disabled or has no URL: the import and pre-fill are then not
// offered. The first release is fetched in the background at startup.
var getCatalogSourceFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*catalog.Source, error) {
	if !conf.Catalog.Enabled || conf.Catalog.URL == "" {
		return nil, nil
	}
	source := catalog.NewSource(conf.Catalog.URL, conf.Catalog.RefreshInterval)
	go source.Refresh(context.WithoutCancel(ctx))
	return source, nil
})
