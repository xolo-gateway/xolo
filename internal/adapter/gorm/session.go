package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IdentitySession struct {
	ID          string `gorm:"primaryKey"`
	IdentityKey string `gorm:"index"`
	ExpiresAt   time.Time
}
type IdentityRevocation struct {
	IdentityKey string `gorm:"primaryKey"`
	RevokedAt   time.Time
}
type LogoutReplay struct {
	Key      string `gorm:"primaryKey"`
	IssuedAt time.Time
}

func identityDigest(issuer, subject string) string {
	h := sha256.Sum256([]byte(issuer + "\x00" + subject))
	return hex.EncodeToString(h[:])
}
func migrateIdentitySessions(db *gorm.DB) error {
	return withoutForeignKeys(db, func() error {
		return db.AutoMigrate(&User{}, &IdentitySession{}, &IdentityRevocation{}, &LogoutReplay{})
	})
}
func (s *Store) OpenSession(ctx context.Context, issuer, subject string, at, expires time.Time) (string, error) {
	if issuer == "" || subject == "" || at.IsZero() || !expires.After(time.Now()) || at.After(time.Now().Add(time.Minute)) {
		return "", port.ErrNotAllowed
	}
	id := uuid.NewString()
	key := identityDigest(issuer, subject)
	err := s.identityTransaction(ctx, func(tx *Store) error {
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		var revoked IdentityRevocation
		err = db.First(&revoked, "identity_key = ?", key).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		if err == nil && !at.After(revoked.RevokedAt) {
			return port.ErrNotAllowed
		}
		return db.Create(&IdentitySession{ID: id, IdentityKey: key, ExpiresAt: expires}).Error
	})
	return id, err
}
func (s *Store) CheckSession(ctx context.Context, id, issuer, subject string) error {
	if id == "" {
		return port.ErrNotAllowed
	}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return err
	}
	var count int64
	if err := db.WithContext(ctx).Model(&IdentitySession{}).Where("id = ? AND identity_key = ? AND expires_at > ?", id, identityDigest(issuer, subject), time.Now()).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return port.ErrNotAllowed
	}
	return nil
}
func (s *Store) CloseSession(ctx context.Context, id string) error {
	return s.identityTransaction(ctx, func(tx *Store) error {
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		return db.Where("id = ?", id).Delete(&IdentitySession{}).Error
	})
}
func (s *Store) RevokeIdentitySessions(ctx context.Context, issuer, subject, jti string, issued time.Time) error {
	if issuer == "" || subject == "" || jti == "" {
		return port.ErrInvalid
	}
	return s.identityTransaction(ctx, func(tx *Store) error {
		db, err := tx.getDatabase(ctx)
		if err != nil {
			return err
		}
		replay := identityDigest(issuer, jti)
		var n int64
		if err := db.Model(&LogoutReplay{}).Where("key = ?", replay).Count(&n).Error; err != nil {
			return err
		}
		if n != 0 {
			return port.ErrAlreadyExists
		}
		if err := db.Create(&LogoutReplay{Key: replay, IssuedAt: issued}).Error; err != nil {
			return err
		}
		key := identityDigest(issuer, subject)
		if err := db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&IdentityRevocation{IdentityKey: key, RevokedAt: time.Now().UTC()}).Error; err != nil {
			return err
		}
		return db.Where("identity_key = ?", key).Delete(&IdentitySession{}).Error
	})
}
