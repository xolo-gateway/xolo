package gorm

import (
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// OpenSQLiteReader opens a second handle on the SQLite database at dsn, for
// the plain reads (see WithReader). In WAL mode its connections read beside
// the single writer connection instead of queueing behind it. Every
// connection is query_only: a write routed there by mistake fails instead of
// racing the writer. Foreign keys are left alone, they only apply to writes.
// The caller sizes the pool.
//
// An in-memory database is private to the connection that opened it: the
// reader would see an empty database, and is refused.
func OpenSQLiteReader(dsn string, busyTimeout time.Duration, config *gorm.Config) (*gorm.DB, error) {
	if IsSQLiteMemoryDSN(dsn) {
		return nil, errors.Errorf("cannot open a reader on in-memory database %q", dsn)
	}
	sqlDB, err := driver.Open(dsn, func(conn *sqlite3.Conn) error {
		if err := conn.BusyTimeout(busyTimeout); err != nil {
			return err
		}
		return conn.Exec("PRAGMA query_only=1")
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}
	db, err := gorm.Open(gormlite.OpenDB(sqlDB), config)
	if err != nil {
		_ = sqlDB.Close()
		return nil, errors.WithStack(err)
	}
	return db, nil
}

// IsSQLiteMemoryDSN reports whether dsn designates an in-memory SQLite
// database. The shared-cache form is included on purpose: shared cache
// serializes its connections on table locks, which defeats the read pool.
func IsSQLiteMemoryDSN(dsn string) bool {
	return dsn == ":memory:" || dsn == "" ||
		strings.HasPrefix(dsn, "file::memory:") ||
		strings.Contains(dsn, "mode=memory")
}
