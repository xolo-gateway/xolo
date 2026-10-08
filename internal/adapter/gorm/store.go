package gorm

import (
	"context"
	"log/slog"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
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
	// recorder collects the identity resources written by a bound store; the
	// owner of the transaction publishes their projections before commit.
	recorder *mutationRecorder
	// identityIssuers returns the issuer each provider's sign-ins prove, so a
	// link and a declared identity designating the same person are recognized.
	// Resolved lazily: discovering them may need the network.
	identityIssuers func() model.IdentityIssuers
	// ownership is the write authority of each family; nil checks nothing.
	ownership model.OwnershipPolicy
}

func (s *Store) issuers() model.IdentityIssuers {
	if s.identityIssuers == nil {
		return nil
	}
	return s.identityIssuers()
}

// recorded runs a write to identity resources together with the publication
// of the projections it changes. track designates the resources, before the
// write. A bound store records into the enclosing transaction instead.
func (s *Store) recorded(ctx context.Context, track func(*mutationRecorder) error, fn func(ctx context.Context, db *gorm.DB) error) error {
	if s.transactionBound {
		if s.recorder != nil {
			if err := track(s.recorder); err != nil {
				return err
			}
		}
		return s.withRetry(ctx, true, fn)
	}
	// The correlation of the published events stays the same across retries.
	ctx = model.EnsureActor(ctx)
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		recorder := newMutationRecorder(db, s.ownership)
		if err := track(recorder); err != nil {
			return err
		}
		if err := fn(ctx, db); err != nil {
			return err
		}
		return recorder.flush(ctx)
	})
}

// tracking designates one resource to record.
func tracking(kind, id string) func(*mutationRecorder) error {
	return func(r *mutationRecorder) error { return r.track(kind, id) }
}

// trackingTree designates one resource and everything removed with it.
func trackingTree(kind, id string) func(*mutationRecorder) error {
	return func(r *mutationRecorder) error {
		if err := r.track(kind, id); err != nil {
			return err
		}
		return r.trackDependents(kind, id)
	}
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
type storeOptions struct {
	autoMigrate     bool
	identityIssuers func() model.IdentityIssuers
	ownership       model.OwnershipPolicy
}

// WithAutoMigrate controls implicit schema changes; explicit Migrate still works.
func WithAutoMigrate(enabled bool) StoreOption {
	return func(opts *storeOptions) { opts.autoMigrate = enabled }
}

// WithIdentityIssuers declares the issuer each authentication provider proves,
// resolved on first use. A provider absent from the map never matches a
// declared identity.
func WithIdentityIssuers(issuers func() model.IdentityIssuers) StoreOption {
	return func(opts *storeOptions) { opts.identityIssuers = issuers }
}

// WithOwnership sets the write authority of each family. Without it, the
// store checks no authority: migrations and offline operator tools hold
// database authority.
func WithOwnership(policy model.OwnershipPolicy) StoreOption {
	return func(opts *storeOptions) { opts.ownership = policy.Effective() }
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
		identityIssuers:    opts.identityIssuers,
		ownership:          opts.ownership,
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
