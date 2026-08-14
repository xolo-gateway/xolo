package gorm

import (
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"gorm.io/gorm"
)

// migrateToDefaultTenant introduces the tenant level above organizations and
// users. Every pre-existing row is attached to a single tenant named after
// model.DefaultTenantSlug, which is the tenant Xolo transparently uses when
// multi-tenancy is disabled — the upgrade is invisible to existing instances.
//
// It is idempotent: the tenant is only created when missing, and the backfill
// only touches rows that carry no tenant yet.
func migrateToDefaultTenant(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&Tenant{}); err != nil {
		return errors.WithStack(err)
	}

	// The columns are added before the entities are migrated: AutoMigrate would
	// otherwise create the composite unique indexes over a column that does not
	// exist yet.
	//
	// They are added nullable rather than through Migrator().AddColumn, which
	// would emit the NOT NULL of the struct tag: SQLite refuses to add a NOT NULL
	// column with no default to a populated table. The constraint is applied by
	// the AutoMigrate at the end of this function, once every row carries a
	// tenant.
	for _, table := range []string{"organizations", "users"} {
		if tx.Migrator().HasTable(table) && !tx.Migrator().HasColumn(table, "tenant_id") {
			stmt := "ALTER TABLE " + tx.Statement.Quote(table) + " ADD COLUMN tenant_id text"
			if err := tx.Exec(stmt).Error; err != nil {
				return errors.WithStack(err)
			}
		}
	}

	tenantID, err := ensureDefaultTenant(tx)
	if err != nil {
		return err
	}

	for _, table := range []string{"organizations", "users"} {
		stmt := "UPDATE " + tx.Statement.Quote(table) + " SET tenant_id = ? WHERE tenant_id IS NULL OR tenant_id = ''"
		if err := tx.Exec(stmt, tenantID).Error; err != nil {
			return errors.WithStack(err)
		}
	}

	// The pre-existing unique indexes are instance-wide; they must go before the
	// tenant-scoped ones can replace them, otherwise two tenants could never
	// share an organization slug or an identity.
	//
	// They are dropped by their literal name, not by struct field: the field now
	// carries the composite index, so asking GORM to resolve the name from the
	// current tag would look up the replacement instead of the index being
	// removed.
	for _, idx := range []struct {
		table string
		name  string
	}{
		{"organizations", "idx_organizations_slug"},
		{"users", "idx_users_email_nonempty"},
	} {
		if !tx.Migrator().HasTable(idx.table) || !tx.Migrator().HasIndex(idx.table, idx.name) {
			continue
		}
		if err := tx.Migrator().DropIndex(idx.table, idx.name); err != nil {
			return errors.WithStack(err)
		}
	}

	return errors.WithStack(tx.AutoMigrate(&Organization{}, &User{}))
}

// ensureDefaultTenant returns the ID of the default tenant, creating it when it
// does not exist yet.
func ensureDefaultTenant(tx *gorm.DB) (string, error) {
	var existing Tenant
	err := tx.Where("slug = ?", model.DefaultTenantSlug).First(&existing).Error
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", errors.WithStack(err)
	}

	now := time.Now()
	tenant := &Tenant{
		ID:          string(model.NewTenantID()),
		CreatedAt:   now,
		UpdatedAt:   now,
		Slug:        model.DefaultTenantSlug,
		Name:        "Default",
		Description: "Tenant hosting every organization of this instance.",
		Active:      1,
	}
	if err := tx.Create(tenant).Error; err != nil {
		return "", errors.WithStack(err)
	}

	return tenant.ID, nil
}

// rollbackDefaultTenant undoes migrateToDefaultTenant.
func rollbackDefaultTenant(tx *gorm.DB) error {
	for _, m := range []any{&Organization{}, &User{}} {
		if !tx.Migrator().HasColumn(m, "tenant_id") {
			continue
		}
		if err := tx.Migrator().DropColumn(m, "tenant_id"); err != nil {
			return errors.WithStack(err)
		}
	}

	return errors.WithStack(tx.Migrator().DropTable("tenants"))
}
