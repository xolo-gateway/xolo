package setup

import (
	"context"
	"net/http"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/http/middleware/bridge"
)

func getBridgeMiddlewareFromConfig(ctx context.Context, conf *config.Config) (func(http.Handler) http.Handler, error) {
	userStore, err := getUserStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	emitter, err := getEventEmitterFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	bridgeMiddleware := bridge.Middleware(userStore, emitter, bridge.Options{
		Managed:         conf.Ownership.Effective()["member"] == model.OwnerControlPlane,
		ActiveByDefault: conf.HTTP.Authn.ActiveByDefault,
		AutoCreateUsers: conf.HTTP.Authn.AutoCreateUsers,
		DefaultAdmins:   conf.HTTP.Authn.DefaultAdmins,
	})

	return bridgeMiddleware, nil
}
