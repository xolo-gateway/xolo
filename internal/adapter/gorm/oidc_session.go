package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// oidcSessionSweepBatch bounds the rows one sweep statement removes.
const oidcSessionSweepBatch = 1000

// OIDCIdentity is the row every session operation of an identity locks first,
// so opening a session and revoking the identity serialize on it alone. It
// holds the revocation watermark: a sign-in started before it is refused.
type OIDCIdentity struct {
	IdentityKey string `gorm:"primaryKey;size:64"`
	RevokedAt   *time.Time
	TouchedAt   time.Time `gorm:"not null;index"`
}

func (OIDCIdentity) TableName() string { return "oidc_identities" }

// OIDCSession is one interactive session, designated by the cookie that
// carries its ID.
type OIDCSession struct {
	ID              string    `gorm:"primaryKey;size:36"`
	IdentityKey     string    `gorm:"not null;index;size:64"`
	TenantID        string    `gorm:"not null;index"`
	AuthenticatedAt time.Time `gorm:"not null"`
	ExpiresAt       time.Time `gorm:"not null;index"`
	CreatedAt       time.Time
}

func (OIDCSession) TableName() string { return "oidc_sessions" }

// OIDCLogoutReplay remembers a processed logout token until it can no longer
// be accepted (see model.LogoutReplayExpiry).
type OIDCLogoutReplay struct {
	ReplayKey string    `gorm:"primaryKey;size:64"`
	ExpiresAt time.Time `gorm:"not null;index"`
}

func (OIDCLogoutReplay) TableName() string { return "oidc_logout_replays" }

// oidcDigest keys an identity, or a logout token, without storing it.
func oidcDigest(issuer, value string) string {
	h := sha256.Sum256([]byte(issuer + "\x00" + value))
	return hex.EncodeToString(h[:])
}

// touchOIDCIdentity creates or touches the identity row, which locks it until
// the end of the transaction: on PostgreSQL the upsert takes the row lock, on
// SQLite the write takes the database writer lock.
func touchOIDCIdentity(db *gorm.DB, key string, now time.Time) error {
	return errors.WithStack(db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "identity_key"}},
		DoUpdates: clause.Assignments(map[string]any{"touched_at": now}),
	}).Create(&OIDCIdentity{IdentityKey: key, TouchedAt: now}).Error)
}

// OpenSession implements port.SessionRegistry.
func (s *Store) OpenSession(ctx context.Context, session model.OIDCSession) (string, error) {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return "", errors.WithStack(err)
	}
	db = db.WithContext(ctx)
	now := db.NowFunc().UTC()
	if session.TenantID == "" || session.Issuer == "" || session.Subject == "" || session.AuthenticatedAt.IsZero() ||
		session.AuthenticatedAt.After(now.Add(model.LogoutClockSkew)) || !session.ExpiresAt.After(now) {
		return "", errors.WithStack(port.ErrNotAllowed)
	}

	id := uuid.NewString()
	key := oidcDigest(session.Issuer, session.Subject)
	err = retryTransaction(ctx, func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := touchOIDCIdentity(tx, key, now); err != nil {
				return err
			}
			var identity OIDCIdentity
			if err := tx.Take(&identity, "identity_key = ?", key).Error; err != nil {
				return errors.WithStack(err)
			}
			if identity.RevokedAt != nil && !session.AuthenticatedAt.After(*identity.RevokedAt) {
				return errors.WithStack(port.ErrNotAllowed)
			}
			return errors.WithStack(tx.Create(&OIDCSession{
				ID:              id,
				IdentityKey:     key,
				TenantID:        string(session.TenantID),
				AuthenticatedAt: session.AuthenticatedAt.UTC(),
				ExpiresAt:       session.ExpiresAt.UTC(),
			}).Error)
		})
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// CheckSession implements port.SessionRegistry. It runs on every request
// carrying a session cookie: one read by primary key, no transaction.
func (s *Store) CheckSession(ctx context.Context, id string, tenant model.TenantID, issuer, subject string) error {
	if id == "" || tenant == "" || issuer == "" || subject == "" {
		return errors.WithStack(port.ErrNotFound)
	}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	db = db.WithContext(ctx)
	var sessions []OIDCSession
	result := db.Select("id").
		Where("id = ? AND identity_key = ? AND tenant_id = ? AND expires_at > ?", id, oidcDigest(issuer, subject), string(tenant), db.NowFunc().UTC()).
		Limit(1).Find(&sessions)
	if result.Error != nil {
		return errors.WithStack(result.Error)
	}
	if len(sessions) == 0 {
		return errors.WithStack(port.ErrNotFound)
	}
	return nil
}

// CloseSession implements port.SessionRegistry.
func (s *Store) CloseSession(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	db = db.WithContext(ctx)
	return retryTransaction(ctx, func() error {
		return errors.WithStack(db.Where("id = ?", id).Delete(&OIDCSession{}).Error)
	})
}

// RevokeIdentitySessions implements port.SessionRegistry.
func (s *Store) RevokeIdentitySessions(ctx context.Context, issuer, subject, jti string, issuedAt, expiresAt time.Time) (bool, error) {
	if issuer == "" || subject == "" || jti == "" || issuedAt.IsZero() {
		return false, errors.WithStack(port.ErrInvalid)
	}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return false, errors.WithStack(err)
	}
	db = db.WithContext(ctx)
	key := oidcDigest(issuer, subject)
	replay := OIDCLogoutReplay{ReplayKey: oidcDigest(issuer, jti), ExpiresAt: model.LogoutReplayExpiry(issuedAt, expiresAt).UTC()}

	var revoked bool
	err = retryTransaction(ctx, func() error {
		revoked = false
		return db.Transaction(func(tx *gorm.DB) error {
			// A concurrent processing of the same token waits on the key, then
			// inserts nothing.
			inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&replay)
			if inserted.Error != nil {
				return errors.WithStack(inserted.Error)
			}
			if inserted.RowsAffected == 0 {
				return nil
			}
			now := tx.NowFunc().UTC()
			if err := touchOIDCIdentity(tx, key, now); err != nil {
				return err
			}
			// The watermark never moves back, whatever the clock of the replica.
			if err := tx.Model(&OIDCIdentity{}).
				Where("identity_key = ? AND (revoked_at IS NULL OR revoked_at < ?)", key, now).
				Update("revoked_at", now).Error; err != nil {
				return errors.WithStack(err)
			}
			if err := tx.Where("identity_key = ?", key).Delete(&OIDCSession{}).Error; err != nil {
				return errors.WithStack(err)
			}
			revoked = true
			return nil
		})
	})
	if err != nil {
		return false, err
	}
	return revoked, nil
}

// SweepSessions implements port.SessionRegistry, by bounded batches each in
// its own statement.
func (s *Store) SweepSessions(ctx context.Context, now time.Time, sessionTTL time.Duration) error {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return errors.WithStack(err)
	}
	db = db.WithContext(ctx)
	now = now.UTC()
	idleBefore := now.Add(-model.OIDCIdentityRetention(sessionTTL))

	steps := []func() *gorm.DB{
		func() *gorm.DB {
			expired := db.Model(&OIDCSession{}).Select("id").Where("expires_at <= ?", now).Limit(oidcSessionSweepBatch)
			return db.Where("id IN (?)", expired).Delete(&OIDCSession{})
		},
		func() *gorm.DB {
			expired := db.Model(&OIDCLogoutReplay{}).Select("replay_key").Where("expires_at <= ?", now).Limit(oidcSessionSweepBatch)
			return db.Where("replay_key IN (?)", expired).Delete(&OIDCLogoutReplay{})
		},
		func() *gorm.DB {
			// The outer predicate repeats the idle condition. On PostgreSQL,
			// under READ COMMITTED, a row an open or a revocation updates
			// meanwhile is checked again against it once its lock is
			// released, and kept. SQLite serializes writers: no open or
			// revocation runs between the subquery and the delete.
			idle := db.Model(&OIDCIdentity{}).Select("identity_key").Where("touched_at < ?", idleBefore).Limit(oidcSessionSweepBatch)
			return db.Where("touched_at < ? AND identity_key IN (?)", idleBefore, idle).Delete(&OIDCIdentity{})
		},
	}
	for _, step := range steps {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			var affected int64
			err := retryTransaction(ctx, func() error {
				result := step()
				affected = result.RowsAffected
				return result.Error
			})
			if err != nil {
				return errors.WithStack(err)
			}
			if affected < oidcSessionSweepBatch {
				break
			}
		}
	}
	return nil
}

var _ port.SessionRegistry = (*Store)(nil)
