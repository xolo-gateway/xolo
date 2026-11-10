package setup

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/http"
	"gorm.io/gorm"
)

// getReadinessCheckFromConfig makes an instance ready while it reaches its
// database through the pool its stores read from: every request but the
// static assets reads it, so an instance that lost it can only answer errors.
// On SQLite that is the reader pool, not the single writer connection, which
// a long write would hold past the probe's deadline.
func getReadinessCheckFromConfig(ctx context.Context, conf *config.Config) (http.ReadinessCheck, error) {
	db, err := getGormReaderFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if db == nil {
		// PostgreSQL, or an in-memory SQLite database: reads share the handle.
		db, err = getGormDatabaseFromConfig(ctx, conf)
		if err != nil {
			return nil, errors.WithStack(err)
		}
	}

	return pingCheck(db)
}

func pingCheck(db *gorm.DB) (http.ReadinessCheck, error) {
	sqlDB, err := db.DB()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return func(ctx context.Context) error {
		return errors.WithStack(sqlDB.PingContext(ctx))
	}, nil
}
