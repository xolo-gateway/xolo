package gorm

import (
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// Dialect names as reported by gorm.DB.Dialector.Name().
const (
	dialectSQLite   = "sqlite"
	dialectPostgres = "postgres"
)

// isSQLite reports whether db talks to a SQLite backend. Used to guard the
// PRAGMA statements and the table-rebuild workarounds that only SQLite needs.
func isSQLite(db *gorm.DB) bool {
	return db.Dialector.Name() == dialectSQLite
}

// isPostgres reports whether db talks to a PostgreSQL backend.
func isPostgres(db *gorm.DB) bool {
	return db.Dialector.Name() == dialectPostgres
}

// outsidePrintableASCII returns a predicate matching the values of column
// holding a character other than printable ASCII. SQL LOWER and TRIM agree
// with Go's case folding and space trimming only on printable ASCII; the
// other values must be compared in Go.
func outsidePrintableASCII(db *gorm.DB, column string) string {
	if isPostgres(db) {
		return column + " ~ '[^ -~]'"
	}
	return column + " GLOB '*[^ -~]*'"
}

// PostgreSQL SQLSTATE codes worth retrying: the transaction can succeed on a
// second attempt without any change to the statements it runs.
var retryablePGCodes = map[string]struct{}{
	"40001": {}, // serialization_failure
	"40P01": {}, // deadlock_detected
	"55P03": {}, // lock_not_available
	"55006": {}, // object_in_use
}

// isRetryableError reports whether err designates a transient contention
// failure (a busy/locked SQLite database, a serialization conflict or deadlock
// on PostgreSQL) that warrants replaying the whole transaction.
func isRetryableError(err error) bool {
	// The driver may return ErrorCode or ExtendedErrorCode directly (not just
	// *sqlite3.Error), notably for BUSY_SNAPSHOT during a read-to-write upgrade.
	if errors.Is(err, sqlite3.BUSY) || errors.Is(err, sqlite3.LOCKED) {
		return true
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		_, ok := retryablePGCodes[pgErr.Code]
		return ok
	}

	return false
}

// isUniqueViolation reports whether err is a unique constraint violation whose
// message references every fragment given. Fragments let a caller distinguish
// which index was hit; they are matched case-insensitively against the driver
// message, which spells the index out differently per backend (SQLite reports
// "users.email", PostgreSQL the index name "idx_users_email_nonempty").
func isUniqueViolation(err error, fragments ...string) bool {
	// A lifecycle guard aborts with a constraint error too on SQLite.
	if isFrozenError(err) {
		return false
	}
	var msg string

	var sqliteErr *sqlite3.Error
	if errors.As(err, &sqliteErr) {
		if sqliteErr.Code() != sqlite3.CONSTRAINT {
			return false
		}
		msg = sqliteErr.Error()
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) {
			return false
		}
		if pgErr.Code != "23505" { // unique_violation
			return false
		}
		msg = pgErr.Message + " " + pgErr.Detail + " " + pgErr.ConstraintName
	}

	msg = strings.ToLower(msg)
	for _, fragment := range fragments {
		if !strings.Contains(msg, strings.ToLower(fragment)) {
			return false
		}
	}

	return true
}

// The lifecycle guards refuse a write to a frozen scope with this message on
// SQLite, and with this SQLSTATE on PostgreSQL.
const (
	frozenGuardMessage = "xolo_resource_deleted"
	frozenGuardCode    = "XO001"
)

// isFrozenError reports whether err is the refusal of a lifecycle guard.
func isFrozenError(err error) bool {
	var sqliteErr *sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code() == sqlite3.CONSTRAINT && strings.Contains(sqliteErr.Error(), frozenGuardMessage)
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == frozenGuardCode
}

// lifecycleError turns the refusal of a lifecycle guard into
// port.ErrResourceDeleted, and leaves any other error as is.
func lifecycleError(err error) error {
	if err != nil && isFrozenError(err) {
		return errors.WithStack(port.ErrResourceDeleted)
	}
	return err
}
