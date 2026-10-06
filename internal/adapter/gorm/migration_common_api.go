package gorm

import (
	"log/slog"

	"github.com/pkg/errors"
	"gorm.io/gorm"
)

const commonAPIMigrationID = "202610070001"

// migrateCommonAPI adds the state of the common provisioning contract: domain
// routing, tenant roles and membership statuses. Column defaults keep the rows
// written by a server that predates them valid.
func migrateCommonAPI(tx *gorm.DB) error {
	slog.WarnContext(tx.Statement.Context, "migration "+commonAPIMigrationID+" requires ALL old servers to be stopped: an old server keeps routing on XOLO_MULTITENANCY_HOST_PATTERN and ignores suspended memberships")
	// Users provisioned ahead of their first sign-in have no identity: the
	// identity key becomes partial so several of them can coexist.
	if err := tx.Exec("DROP INDEX IF EXISTS " + tx.Statement.Quote("idx_users_tenant_identity")).Error; err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(tx.AutoMigrate(&User{}, &Membership{}, &Domain{}, &DomainRouting{}))
}
