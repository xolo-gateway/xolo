package gorm

import (
	"context"
	"log/slog"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

type Store struct {
	getDatabase func(ctx context.Context) (*gorm.DB, error)
}

// withRetry runs fn, replaying it with an exponential backoff while the
// backend reports a transient contention failure (see isRetryableError).
func (s *Store) withRetry(ctx context.Context, withTx bool, fn func(ctx context.Context, db *gorm.DB) error) error {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}

	backoff := 500 * time.Millisecond
	maxRetries := 10
	retries := 0

	for {
		var err error
		if withTx {
			err = db.Transaction(func(tx *gorm.DB) error {
				if err := fn(ctx, tx); err != nil {
					return errors.WithStack(err)
				}

				return nil
			})
		} else {
			err = fn(ctx, db)
		}

		if err != nil {
			if retries >= maxRetries {
				return errors.WithStack(err)
			}

			if isRetryableError(err) {
				slog.DebugContext(ctx, "transaction failed, will retry", slog.Int("retries", retries), slog.Duration("backoff", backoff), slog.Any("error", errors.WithStack(err)))

				retries++
				time.Sleep(backoff)
				backoff *= 2
				continue
			}

			return errors.WithStack(err)
		}

		return nil
	}
}

func NewStore(db *gorm.DB) *Store {
	return &Store{
		getDatabase: createGetDatabase(db),
	}
}

// Migrate applies all pending schema migrations. NewStore still migrates
// lazily on the first store operation, while application setup can call this
// method to guarantee the schema is ready before starting background work.
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.getDatabase(ctx)
	return errors.WithStack(err)
}

var _ port.UserStore = &Store{}
