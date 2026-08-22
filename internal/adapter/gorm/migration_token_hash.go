package gorm

import (
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/crypto"
	"gorm.io/gorm"
)

// migrateAuthTokensToHashes replaces the clear-text API keys stored in
// auth_tokens by their SHA-256 hash.
//
// The clear-text values are still readable at this point, so every existing key
// is hashed in place and keeps working: no integration breaks. What the
// migration does end is the ability to read a key back out of the database —
// after it runs, a key that was not copied at creation is unrecoverable.
//
// It is idempotent: a value that already looks like a hash is left alone, so a
// re-run cannot hash a hash and lock everyone out.
func migrateAuthTokensToHashes(tx *gorm.DB) error {
	if !tx.Migrator().HasTable("auth_tokens") {
		return nil
	}

	type row struct {
		ID    string
		Value string
	}

	var rows []row
	if err := tx.Table("auth_tokens").Select("id", "value").Find(&rows).Error; err != nil {
		return errors.WithStack(err)
	}

	for _, r := range rows {
		if isHashedToken(r.Value) {
			continue
		}

		hashed := crypto.HashToken(r.Value)
		if err := tx.Table("auth_tokens").Where("id = ?", r.ID).Update("value", hashed).Error; err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

// isHashedToken reports whether the value is already a SHA-256 hex digest.
func isHashedToken(value string) bool {
	if len(value) != 64 {
		return false
	}

	for _, c := range value {
		isHexDigit := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !isHexDigit {
			return false
		}
	}

	return true
}
