package setup

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/pkg/errors"
	gormAdapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// IsPostgresDSN reports whether dsn addresses a PostgreSQL server. Both the URL
// form ("postgres://user:pass@host/db") and the libpq keyword form
// ("host=... user=... dbname=...") are recognized; anything else is treated as
// a SQLite file path, which keeps the historical default working.
func IsPostgresDSN(dsn string) bool {
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
	usePostgres := IsPostgresDSN(dsn)

	var dialector gorm.Dialector
	if usePostgres {
		dialector = postgres.Open(dsn)
	} else {
		dialector = gormlite.Open(dsn)
	}

	db, err := gorm.Open(dialector, newGormConfig(conf))
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
	// through. Plain reads go through the reader (see getGormReaderFromConfig).
	internalDB.SetMaxOpenConns(1)

	if err := db.Exec(fmt.Sprintf("PRAGMA journal_mode=wal; PRAGMA foreign_keys=on; PRAGMA busy_timeout=%d", sqliteBusyTimeout.Milliseconds())).Error; err != nil {
		return nil, errors.WithStack(err)
	}

	slog.DebugContext(ctx, "using sqlite storage backend")

	return db, nil
})

// sqliteBusyTimeout bounds how long a SQLite connection waits for a lock
// before reporting the database busy.
const sqliteBusyTimeout = 5 * time.Second

// getGormReaderFromConfig returns the handle the store serves plain reads
// from, or nil when they share the database handle. On SQLite the database
// has a single connection, which a long read would hold against every other
// query, the proxy path included: the reader is a pool of read-only
// connections on the same file. PostgreSQL already has a pool.
var getGormReaderFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*gorm.DB, error) {
	dsn := conf.Storage.Database.DSN
	if IsPostgresDSN(dsn) {
		return nil, nil
	}
	// A private in-memory database is unreachable from another connection.
	if gormAdapter.IsSQLiteMemoryDSN(dsn) {
		slog.WarnContext(ctx, "in-memory sqlite database, reads share the writer connection")
		return nil, nil
	}
	// The writer enables WAL mode: without it, readers would block the writer.
	if _, err := getGormDatabaseFromConfig(ctx, conf); err != nil {
		return nil, errors.WithStack(err)
	}

	db, err := gormAdapter.OpenSQLiteReader(dsn, sqliteBusyTimeout, newGormConfig(conf))
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

	pool := conf.Storage.Database.Pool
	internalDB.SetMaxOpenConns(pool.MaxOpenConns)
	internalDB.SetMaxIdleConns(pool.MaxIdleConns)
	internalDB.SetConnMaxLifetime(pool.ConnMaxLifetime)

	slog.DebugContext(ctx, "using sqlite read pool",
		slog.Int("maxOpenConns", pool.MaxOpenConns),
		slog.Int("maxIdleConns", pool.MaxIdleConns),
	)

	return db, nil
})

func newGormConfig(conf *config.Config) *gorm.Config {
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

	// A lookup that finds nothing is an answer, not an error: the stores turn it
	// into port.ErrNotFound. Logging it buried the slow queries under hundreds
	// of lines an hour. No colors: the escape codes end up in collected logs.
	return &gorm.Config{
		Logger: logger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logLevel,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		}),
	}
}
