package setup

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// OpenOfflineDatabase opens the database of an offline operator command
// (xolo-migrate, xolo-adoption). A read-only SQLite database is opened in
// read-only mode; the caller imports the SQLite driver.
func OpenOfflineDatabase(dsn string, readOnly bool) (*gorm.DB, error) {
	var dialect gorm.Dialector
	if IsPostgresDSN(dsn) {
		dialect = postgres.Open(dsn)
	} else {
		if readOnly {
			if !strings.HasPrefix(dsn, "file:") {
				path, err := filepath.Abs(dsn)
				if err != nil {
					return nil, err
				}
				dsn = (&url.URL{Scheme: "file", Path: path}).String()
			}
			u, err := url.Parse(dsn)
			if err != nil {
				return nil, fmt.Errorf("invalid SQLite file URI")
			}
			q := u.Query()
			q.Set("mode", "ro")
			u.RawQuery = q.Encode()
			dsn = u.String()
		}
		dialect = gormlite.Open(dsn)
	}
	db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		// Driver errors can include credentials from the DSN.
		return nil, fmt.Errorf("cannot open database; check XOLO_STORAGE_DATABASE_DSN and access permissions")
	}
	if !IsPostgresDSN(dsn) {
		pool, err := db.DB()
		if err != nil {
			return nil, err
		}
		pool.SetMaxOpenConns(1)
		if err := db.Exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000").Error; err != nil {
			pool.Close()
			return nil, err
		}
	}
	return db, nil
}
