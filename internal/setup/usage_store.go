package setup

import (
	"context"
	"log/slog"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/adapter/cache"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

var getUsageStoreFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (port.UsageStore, error) {
	store, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var usageStore port.UsageStore = store

	// The cache wraps the store so that recording usage and checking a budget go
	// through the same instance: the increments applied on a write are what keep
	// the totals read on the next request from being stale.
	quotaCache := conf.Storage.Database.Cache.Quota
	if quotaCache.Enabled && quotaCache.TTL > 0 {
		slog.DebugContext(ctx, "using cached usage store",
			slog.Duration("ttl", quotaCache.TTL),
			slog.Int("cache_size", quotaCache.Size))
		usageStore = cache.NewUsageStore(usageStore, cache.NewMemoryCache(quotaCache.Size), quotaCache.TTL)
	}

	return usageStore, nil
})
