package setup

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/http"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// getReadinessCheckFromConfig makes an instance ready while it reaches its
// database.
//
// On PostgreSQL the ping goes through a connection of its own, outside the
// pool the stores share: a pool saturated by traffic would otherwise make
// every replica unready at the same moment, and pull them all out of the load
// balancer when they are needed most. The probe measures whether the database
// answers, not whether the instance is busy.
//
// On SQLite it goes through the reader pool, not the single writer
// connection, which a long write would hold past the probe's deadline.
func getReadinessCheckFromConfig(ctx context.Context, conf *config.Config) (http.ReadinessCheck, error) {
	if IsPostgresDSN(conf.Storage.Database.DSN) {
		return postgresReadinessCheck(conf)
	}

	reader, err := getGormReaderFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if reader != nil {
		return pingCheck(reader)
	}
	// An in-memory SQLite database: reads share the writer connection.
	writer, err := getGormDatabaseFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return pingCheck(writer)
}

// postgresReadinessCheck pings through a pool of one connection of its own.
func postgresReadinessCheck(conf *config.Config) (http.ReadinessCheck, error) {
	db, err := gorm.Open(postgres.Open(conf.Storage.Database.DSN), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, errors.WithStack(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, errors.WithStack(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(conf.Storage.Database.Pool.ConnMaxLifetime)
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
