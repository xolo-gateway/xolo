package setup

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/http"
)

// getReadinessCheckFromConfig makes an instance ready while it reaches its
// database through the pool its stores share: every request but the static
// assets needs it, so an instance that lost it can only answer errors.
func getReadinessCheckFromConfig(ctx context.Context, conf *config.Config) (http.ReadinessCheck, error) {
	db, err := getGormDatabaseFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return func(ctx context.Context) error {
		return errors.WithStack(sqlDB.PingContext(ctx))
	}, nil
}
