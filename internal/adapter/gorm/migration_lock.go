package gorm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Serialize startup before inspecting migration history. The complete upgrade,
// including its markers, commits atomically. SQLite's write lock and PostgreSQL's
// advisory transaction lock also cover fresh installs and concurrent replicas.
func withMigrationLock(ctx context.Context, db *gorm.DB, migrate func(*gorm.DB) error) error {
	for attempt := 0; ; attempt++ {
		err := db.WithContext(ctx).Connection(func(conn *gorm.DB) (migrationErr error) {
			conn = conn.Session(&gorm.Session{NewDB: true})
			if isSQLite(conn) {
				// Table rebuilds require this outside the transaction. Restore the
				// connection setting even when migration or commit fails.
				var enabled int
				if err := conn.Raw("PRAGMA foreign_keys").Scan(&enabled).Error; err != nil {
					return err
				}
				if err := conn.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
					return err
				}
				defer func() {
					restoreErr := conn.WithContext(context.WithoutCancel(ctx)).Exec(fmt.Sprintf("PRAGMA foreign_keys = %d", enabled)).Error
					migrationErr = errors.Join(migrationErr, restoreErr)
				}()
			}
			return conn.Transaction(func(tx *gorm.DB) error {
				if isPostgres(tx) {
					if err := tx.Exec("SELECT pg_advisory_xact_lock(867530902)").Error; err != nil {
						return err
					}
				} else {
					// This is the first statement: acquire the writer lock before
					// taking a read snapshot, including when the table already exists.
					if err := tx.Exec("CREATE TABLE IF NOT EXISTS migration_lock (id integer PRIMARY KEY)").Error; err != nil {
						return err
					}
					if err := tx.Exec("INSERT INTO migration_lock (id) VALUES (1) ON CONFLICT DO NOTHING").Error; err != nil {
						return err
					}
					if err := tx.Exec("UPDATE migration_lock SET id = id WHERE id = 1").Error; err != nil {
						return err
					}
				}
				if err := migrate(tx); err != nil {
					return err
				}
				if isSQLite(tx) {
					var violations []map[string]any
					if err := tx.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
						return err
					}
					if len(violations) != 0 {
						return fmt.Errorf("migration foreign key check failed")
					}
				}
				return nil
			})
		})
		if err == nil || !isRetryableError(err) || attempt >= 10 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
