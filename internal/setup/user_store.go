package setup

import (
	"context"
	"log/slog"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

var getUserStoreFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (port.UserStore, error) {
	var (
		store port.UserStore
		err   error
	)

	store, err = getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Transaction-bound provisioning bypasses decorators, including local cache
	// invalidation. Until revisions are carried by cache entries, all identity
	// reads must observe the database, including writes from other replicas.
	if conf.Storage.Database.Cache.Users.Enabled {
		slog.InfoContext(ctx, "user cache bypassed to keep transactional identity writes visible across replicas")
	}
	return store, nil
})
