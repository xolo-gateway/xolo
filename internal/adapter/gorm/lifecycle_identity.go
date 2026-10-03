package gorm

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RetiredLogin prevents an old cookie or a delayed login from auto-creating a
// purged account. Keys are tenant-bound HMACs, with no contact/identity text.
// Explicit provisioning of a fresh account is a deliberate new access grant.
type RetiredLogin struct {
	TenantID string `gorm:"primaryKey"`
	Key      string `gorm:"primaryKey"`
}

func retiredLoginKey(secret, kind, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(kind + "\x00" + value))
	return hex.EncodeToString(mac.Sum(nil))
}
func (s *Store) retireLoginRows(db *gorm.DB, rows []map[string]any) error {
	var feed CommonFeed
	if err := db.First(&feed, 1).Error; err != nil {
		return err
	}
	for _, row := range rows {
		tid, _ := row["tenant_id"].(string)
		provider, _ := row["provider"].(string)
		subject, _ := row["subject"].(string)
		issuer, _ := row["identity_issuer"].(string)
		declaredSubject, _ := row["identity_subject"].(string)
		email, _ := row["email"].(string)
		values := map[string]string{"email": model.NormalizeEmail(email)}
		if provider != "" && subject != "" {
			values["identity"] = s.identityIssuer(provider) + "\x00" + subject
		}
		if issuer != "" && declaredSubject != "" {
			values["declaration"] = issuer + "\x00" + declaredSubject
		}
		for kind, value := range values {
			if value == "" {
				continue
			}
			if kind == "declaration" {
				kind = "identity"
			}
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&RetiredLogin{TenantID: tid, Key: retiredLoginKey(feed.CursorSecret, kind, value)}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) checkRetiredLogin(ctx context.Context, u model.User) error {
	db, err := s.getDatabase(ctx)
	if err != nil {
		return err
	}
	var feed CommonFeed
	if err = db.First(&feed, 1).Error; err != nil {
		return err
	}
	keys := []string{retiredLoginKey(feed.CursorSecret, "identity", s.identityIssuer(u.Provider())+"\x00"+u.Subject())}
	if u.Email() != "" {
		keys = append(keys, retiredLoginKey(feed.CursorSecret, "email", model.NormalizeEmail(u.Email())))
	}
	var n int64
	if err = db.Model(&RetiredLogin{}).Where("tenant_id = ? AND key IN ?", string(u.TenantID()), keys).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return port.ErrResourceDeleted
	}
	return nil
}
