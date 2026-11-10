package setup

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/adapter/cache"
	eventsAdapter "github.com/xolo-gateway/xolo/internal/adapter/events"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
)

var getApplicationStoreFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (port.ApplicationStore, error) {
	store, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	emitter, err := getEventEmitterFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var applications port.ApplicationStore = eventsAdapter.NewApplicationStore(store, emitter)
	// The token cache carries each application token's application: changing
	// one must drop its tokens there.
	users, err := getUserStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if cached, ok := users.(*cache.UserStore); ok {
		applications = cache.NewApplicationStore(applications, cached)
	}
	return applications, nil
})
