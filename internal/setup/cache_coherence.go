package setup

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/adapter/cache"
	gormAdapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/config"
)

// getCacheCoherenceFromConfig creates the cross-instance cache invalidation and
// starts its poll, or returns nil when it is turned off. Created at most once
// per config, so every cached store registers with the same one.
var getCacheCoherenceFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*cache.Coherence, error) {
	interval := conf.Storage.Database.Cache.Coherence.Interval
	if interval <= 0 {
		return nil, nil
	}

	db, err := getGormDatabaseFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	coherence := cache.NewCoherence(gormAdapter.NewCacheGenerationStore(db), interval)

	// Stops with the server: ctx is the root context the instance runs on.
	go func() { _ = coherence.Run(ctx) }()

	return coherence, nil
})
