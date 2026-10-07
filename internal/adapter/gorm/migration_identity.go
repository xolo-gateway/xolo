package gorm

import (
	"log/slog"

	"github.com/pkg/errors"
	"gorm.io/gorm"
)

const identityMigrationID = "202610100001"

// declaredIdentityIndex keeps a declared identity unique per tenant.
const declaredIdentityIndex = "idx_declared_identity"

// migrateIdentity adds the identity provisioning declares for a member. No row
// declares one yet, so no projection changes and nothing is published.
func migrateIdentity(tx *gorm.DB) error {
	slog.WarnContext(tx.Statement.Context, "migration "+identityMigrationID+" requires ALL old servers to be stopped: an old server ignores declared identities at sign-in")
	return errors.WithStack(tx.AutoMigrate(&User{}))
}

// rollbackIdentity drops the declarations only while there are none: removing
// one would silently detach the member it designates.
func rollbackIdentity(tx *gorm.DB) error {
	var declared int64
	if err := tx.Model(&User{}).Where("identity_issuer <> ''").Count(&declared).Error; err != nil {
		return errors.WithStack(err)
	}
	if declared != 0 {
		return errors.Errorf("identity migration cannot be rolled back: %d members have a declared identity", declared)
	}
	if err := tx.Exec("DROP INDEX IF EXISTS " + tx.Statement.Quote(declaredIdentityIndex)).Error; err != nil {
		return errors.WithStack(err)
	}
	return withoutForeignKeys(tx, func() error {
		for _, column := range []string{"identity_issuer", "identity_subject"} {
			if err := tx.Migrator().DropColumn(&User{}, column); err != nil {
				return errors.WithStack(err)
			}
		}
		return nil
	})
}
