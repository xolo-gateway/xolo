package setup

import (
	"context"
	"log/slog"
	"strings"

	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// isPostgresDSN reports whether dsn addresses a PostgreSQL server. Both the URL
// form ("postgres://user:pass@host/db") and the libpq keyword form
// ("host=... user=... dbname=...") are recognized; anything else is treated as
// a SQLite file path, which keeps the historical default working.
func isPostgresDSN(dsn string) bool {
	trimmed := strings.TrimSpace(dsn)

	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}

	// libpq keyword/value form: require an explicit host or dbname keyword so a
	// relative SQLite path never matches.
	for _, keyword := range []string{"host=", "dbname=", "postgres="} {
		if strings.HasPrefix(trimmed, keyword) || strings.Contains(trimmed, " "+keyword) {
			return true
		}
	}

	return false
}

var getGormDatabaseFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*gorm.DB, error) {
	dsn := conf.Storage.Database.DSN
	usePostgres := isPostgresDSN(dsn)

	var dialector gorm.Dialector
	if usePostgres {
		dialector = postgres.Open(dsn)
	} else {
		dialector = gormlite.Open(dsn)
	}

	// GORM's Info level prints every statement: at the default XOLO_LOGGER_LEVEL
	// that flooded production logs with tens of thousands of lines a day and
	// rotated the useful ones away within days. Statements are only worth it
	// while debugging; Info keeps errors and slow queries.
	var logLevel logger.LogLevel
	switch {
	case slog.Level(conf.Logger.Level) <= slog.LevelDebug:
		logLevel = logger.Info
	case slog.Level(conf.Logger.Level) <= slog.LevelInfo:
		logLevel = logger.Warn
	default:
		logLevel = logger.Error
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if slog.Level(conf.Logger.Level) == slog.LevelDebug {
		db = db.Debug()
	}

	internalDB, err := db.DB()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if usePostgres {
		pool := conf.Storage.Database.Pool
		internalDB.SetMaxOpenConns(pool.MaxOpenConns)
		internalDB.SetMaxIdleConns(pool.MaxIdleConns)
		internalDB.SetConnMaxLifetime(pool.ConnMaxLifetime)

		slog.DebugContext(ctx, "using postgresql storage backend",
			slog.Int("maxOpenConns", pool.MaxOpenConns),
			slog.Int("maxIdleConns", pool.MaxIdleConns),
		)

		return db, nil
	}

	// SQLite: a single writer connection plus WAL keeps the "database is
	// locked" window small; the store layer retries whatever still slips
	// through.
	internalDB.SetMaxOpenConns(1)

	if err := db.Exec("PRAGMA journal_mode=wal; PRAGMA foreign_keys=on; PRAGMA busy_timeout=5000").Error; err != nil {
		return nil, errors.WithStack(err)
	}

	slog.DebugContext(ctx, "using sqlite storage backend")

	return db, nil
})
