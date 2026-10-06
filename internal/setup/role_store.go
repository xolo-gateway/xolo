package setup

import (
	"context"

	eventsAdapter "github.com/xolo-gateway/xolo/internal/adapter/events"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
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
	// The raw gorm store also implements OrgStore.GetMembership, used to resolve
	// the org/user for membership-role changes.
	return eventsAdapter.NewRoleStore(store, emitter, store), nil
})
