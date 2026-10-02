package setup

import (
	"context"
	"net/http"

	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/http/middleware/bridge"
	"github.com/pkg/errors"
)

func getBridgeMiddlewareFromConfig(ctx context.Context, conf *config.Config) (func(http.Handler) http.Handler, error) {
	userStore, err := getUserStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	inviteStore, err := getInviteStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	emitter, err := getEventEmitterFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	bridgeMiddleware := bridge.Middleware(userStore, inviteStore, emitter, bridge.Options{
		ActiveByDefault: conf.HTTP.Authn.ActiveByDefault,
		AutoCreateUsers: conf.HTTP.Authn.AutoCreateUsers,
		DefaultAdmins:   conf.HTTP.Authn.DefaultAdmins,
	})

	return bridgeMiddleware, nil
}
