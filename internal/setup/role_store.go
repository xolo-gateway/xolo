package setup

import (
	"context"

	"github.com/pkg/errors"
	eventsAdapter "github.com/xolo-gateway/xolo/internal/adapter/events"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

var getRoleStoreFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (port.RoleStore, error) {
	store, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	emitter, err := getEventEmitterFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return eventsAdapter.NewApplicationRoleStore(store, emitter, store), nil
})
