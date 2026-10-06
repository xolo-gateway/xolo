package setup

import (
	"context"

	"github.com/pkg/errors"
	eventsAdapter "github.com/xolo-gateway/xolo/internal/adapter/events"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

var getInviteStoreFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (port.InviteStore, error) {
	store, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	emitter, err := getEventEmitterFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return eventsAdapter.NewInviteStore(store, emitter), nil
})

var getInvitationServiceFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*service.InvitationService, error) {
	store, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	emitter, err := getEventEmitterFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return service.NewInvitationService(eventsAdapter.NewInvitationTransaction(store, emitter)), nil
})
