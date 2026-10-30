package setup

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// getCatalogFollowerFromConfig starts following the releases of the model
// catalog, or returns nil when the catalog or following it is disabled.
// Every replica follows: following is idempotent.
var getCatalogFollowerFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*service.CatalogFollower, error) {
	source, err := getCatalogSourceFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if source == nil || !conf.Catalog.Follow {
		return nil, nil
	}
	providerStore, err := getProviderStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	origins, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	rates, err := getExchangeRateServiceFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	follower := service.NewCatalogFollower(providerStore, origins, rates)
	go follower.Run(context.WithoutCancel(ctx), source, conf.Catalog.RefreshInterval)
	return follower, nil
})
