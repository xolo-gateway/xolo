package setup

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

var NewLifecycleServiceFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*service.LifecycleService, error) {
	if !conf.Lifecycle.Enabled {
		return nil, nil
	}
	if err := conf.Lifecycle.Validate(); err != nil {
		return nil, err
	}
	store, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, err
	}
	return service.NewLifecycleService(store, conf.Lifecycle.Retention, conf.Lifecycle.PollInterval), nil
})
