package setup

import (
	"context"
	"log/slog"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/adapter/cache"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

var getUserStoreFromConfig = createFromConfigOnce(createUserStoreFromConfig)

func createUserStoreFromConfig(ctx context.Context, conf *config.Config) (port.UserStore, error) {
	var (
		store port.UserStore
		err   error
	)

	store, err = getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if conf.Storage.Database.Cache.Users.Enabled {
		slog.DebugContext(ctx, "using cached user store", slog.Duration("ttl", conf.Storage.Database.Cache.Users.TTL), slog.Int("cache_size", conf.Storage.Database.Cache.Users.Size))
		store = cache.NewUserStore(store, conf.Storage.Database.Cache.Users.Size, conf.Storage.Database.Cache.Users.TTL)
	}

	return store, nil
}
