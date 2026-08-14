package setup

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/token"
	"github.com/pkg/errors"
)

func getTokenAuthnHandlerFromConfig(ctx context.Context, conf *config.Config) (*token.Handler, error) {
	sessionStore, err := getSessionStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	userStore, err := getUserStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	orgStore, err := getOrgStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	handler := token.NewHandler(sessionStore, userStore, orgStore)

	return handler, nil
}
