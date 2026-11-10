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
// database (see readinessDB for the handle the ping goes through).
func getReadinessCheckFromConfig(ctx context.Context, conf *config.Config) (http.ReadinessCheck, error) {
	var reader, writer *gorm.DB
	if !IsPostgresDSN(conf.Storage.Database.DSN) {
		var err error
		if reader, err = getGormReaderFromConfig(ctx, conf); err != nil {
			return nil, errors.WithStack(err)
		}
		if reader == nil {
			if writer, err = getGormDatabaseFromConfig(ctx, conf); err != nil {
				return nil, errors.WithStack(err)
			}
		}
	}

	db, err := readinessDB(conf, reader, writer)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return pingCheck(db)
}

// readinessDB picks the handle the readiness ping goes through.
//
// On PostgreSQL, a connection of its own, outside the pool the stores share:
// a pool saturated by traffic would otherwise make every replica unready at
// the same moment, and pull them all out of the load balancer when they are
// needed most. The probe measures whether the database answers, not whether
// the instance is busy.
//
// On SQLite, the reader pool, not the single writer connection, which a long
// write would hold past the probe's deadline; the writer only for an
// in-memory database, which has no reader.
func readinessDB(conf *config.Config, reader, writer *gorm.DB) (*gorm.DB, error) {
	if IsPostgresDSN(conf.Storage.Database.DSN) {
		return postgresReadinessDB(conf)
	}
	if reader != nil {
		return reader, nil
	}
	if writer == nil {
		return nil, errors.New("readiness: no database handle")
	}
	return writer, nil
}

// postgresReadinessDB opens a pool of one connection. It does not connect
// here: the first probe does.
func postgresReadinessDB(conf *config.Config) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(conf.Storage.Database.DSN), &gorm.Config{
		Logger:                 logger.Discard,
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
	})
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
	return db, nil
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
