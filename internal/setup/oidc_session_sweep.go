package setup

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/oidc"
)

const oidcSessionSweepInterval = 10 * time.Minute

// startOIDCSessionSweepFromConfig starts the cleanup of the OIDC session
// registry until ctx is done.
var startOIDCSessionSweepFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (struct{}, error) {
	store, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return struct{}{}, errors.WithStack(err)
	}
	ttl := conf.HTTP.Session.Cookie.MaxAge
	if ttl <= 0 {
		ttl = oidc.DefaultSessionTTL
	}
	go service.RunSessionSweep(ctx, store, ttl, oidcSessionSweepInterval)
	return struct{}{}, nil
})
