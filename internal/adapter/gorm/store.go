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
	getDatabase        func(ctx context.Context) (*gorm.DB, error)
	initializeDatabase func(context.Context, bool) (*gorm.DB, error)
	// Invitation and provisioning callbacks own the transaction and retry boundary: on a
	// transaction-bound store, withRetry runs fn exactly once
	// on that transaction and never opens its own.
	transactionBound bool
}

// withRetry runs fn, replaying it with an exponential backoff while the
// backend reports a transient contention failure (see isRetryableError).
func (s *Store) withRetry(ctx context.Context, withTx bool, fn func(ctx context.Context, db *gorm.DB) error) error {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	if s.transactionBound {
		// db is already bound: withTx is ignored and a
		// failure goes back to the transaction adapter, which replays the
		// whole callback.
		return fn(ctx, db.WithContext(ctx))
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

type StoreOption func(*storeOptions)
type storeOptions struct{ autoMigrate bool }

// WithAutoMigrate controls implicit schema changes; explicit Migrate still works.
func WithAutoMigrate(enabled bool) StoreOption {
	return func(opts *storeOptions) { opts.autoMigrate = enabled }
}

func NewStore(db *gorm.DB, options ...StoreOption) *Store {
	opts := storeOptions{autoMigrate: true}
	for _, option := range options {
		option(&opts)
	}
	initialize := createDatabaseInitializer(db)
	return &Store{
		initializeDatabase: initialize,
		getDatabase:        func(ctx context.Context) (*gorm.DB, error) { return initialize(ctx, opts.autoMigrate) },
	}
}

// Migrate explicitly applies pending migrations, independently of WithAutoMigrate.
// A successful migration is cached for this store; failures can be retried.
// Application setup calls CheckSchema instead when automatic migration is disabled.
func (s *Store) Migrate(ctx context.Context) error {
	if s.transactionBound {
		return errors.New("cannot migrate schema within a transaction")
	}
	_, err := s.initializeDatabase(ctx, true)
	return errors.WithStack(err)
}

// CheckSchema validates migration history without changing the database.
// A successful check is cached independently of Migrate; failures can be retried.
func (s *Store) CheckSchema(ctx context.Context) error {
	if s.transactionBound {
		return errors.New("cannot check schema within a transaction")
	}
	_, err := s.initializeDatabase(ctx, false)
	return errors.WithStack(err)
}

var _ port.UserStore = &Store{}
