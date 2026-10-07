package gorm

import (
	"log/slog"

	"github.com/pkg/errors"
	"gorm.io/gorm"
)

const oidcSessionsMigrationID = "202610110001"

// migrateOIDCSessions creates the registry of interactive OIDC sessions. The
// cookies issued before it carry no session: their holders sign in again.
func migrateOIDCSessions(tx *gorm.DB) error {
	slog.WarnContext(tx.Statement.Context, "migration "+oidcSessionsMigrationID+" requires ALL old servers to be stopped: an old server accepts revoked OIDC sessions and issues cookies the new ones refuse")
	return errors.WithStack(tx.AutoMigrate(&OIDCIdentity{}, &OIDCSession{}, &OIDCLogoutReplay{}))
}

// rollbackOIDCSessions drops the registry: it only holds sessions, which an
// older server neither reads nor needs.
func rollbackOIDCSessions(tx *gorm.DB) error {
	return errors.WithStack(tx.Migrator().DropTable(&OIDCLogoutReplay{}, &OIDCSession{}, &OIDCIdentity{}))
}
