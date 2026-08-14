package gorm

import (
	"testing"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// legacyOrganization is the organizations table as it stood before tenants:
// the slug carried an instance-wide unique index and there was no tenant_id.
type legacyOrganization struct {
	ID          string `gorm:"primaryKey;autoIncrement:false"`
	Slug        string `gorm:"uniqueIndex;not null"`
	Name        string `gorm:"not null"`
	Description string
	Active      int
	Currency    string `gorm:"not null;default:'USD'"`
}

// legacyUser is the users table before tenants: the identity was unique
// instance-wide.
type legacyUser struct {
	ID          string `gorm:"primaryKey;autoIncrement:false"`
	Subject     string `gorm:"index"`
	Provider    string `gorm:"index"`
	DisplayName string
	Email       string `gorm:"uniqueIndex:idx_users_email_nonempty,where:email != ''"`
	Active      bool
}

// TestMigrateToDefaultTenant exercises the upgrade path of an existing
// instance: every organization and user must end up attached to the default
// tenant, without anyone having to touch a configuration file.
func TestMigrateToDefaultTenant(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	if err := db.Table("organizations").AutoMigrate(&legacyOrganization{}); err != nil {
		t.Fatalf("migrate legacy organizations: %v", err)
	}
	if err := db.Table("users").AutoMigrate(&legacyUser{}); err != nil {
		t.Fatalf("migrate legacy users: %v", err)
	}

	orgs := []legacyOrganization{
		{ID: "org-1", Slug: "acme", Name: "Acme", Active: 1, Currency: "EUR"},
		{ID: "org-2", Slug: "globex", Name: "Globex", Active: 1, Currency: "USD"},
	}
	for _, org := range orgs {
		if err := db.Table("organizations").Create(&org).Error; err != nil {
			t.Fatalf("create legacy org %q: %v", org.Slug, err)
		}
	}

	users := []legacyUser{
		{ID: "user-1", Provider: "openid-connect", Subject: "sub-1", Email: "jean@acme.tld", Active: true},
		{ID: "user-2", Provider: "openid-connect", Subject: "sub-2", Email: "marie@globex.tld", Active: true},
	}
	for _, user := range users {
		if err := db.Table("users").Create(&user).Error; err != nil {
			t.Fatalf("create legacy user %q: %v", user.Subject, err)
		}
	}

	if err := withoutForeignKeys(db, func() error { return migrateToDefaultTenant(db) }); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var tenant Tenant
	if err := db.First(&tenant, "slug = ?", model.DefaultTenantSlug).Error; err != nil {
		t.Fatalf("default tenant should exist: %v", err)
	}

	for _, table := range []string{"organizations", "users"} {
		var orphans int64
		err := db.Table(table).Where("tenant_id IS NULL OR tenant_id != ?", tenant.ID).Count(&orphans).Error
		if err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if orphans != 0 {
			t.Errorf("%s: %d rows are not attached to the default tenant", table, orphans)
		}
	}

	// The instance-wide unique indexes must be gone, otherwise a second tenant
	// could never reuse a slug or an identity.
	store := NewStore(db)
	ctx := t.Context()

	other := model.NewTenant("other", "Other", "")
	if err := store.CreateTenant(ctx, other); err != nil {
		t.Fatalf("create second tenant: %v", err)
	}

	if err := store.CreateOrg(ctx, model.NewOrganization(other.ID(), "acme", "Acme", "")); err != nil {
		t.Errorf("the slug %q should be reusable in another tenant: %v", "acme", err)
	}
	if err := store.SaveUser(ctx, model.NewUser(other.ID(), "openid-connect", "sub-1", "jean@acme.tld", "Jean", true)); err != nil {
		t.Errorf("the identity should be reusable in another tenant: %v", err)
	}

	// Re-running the migration must be a no-op: it is replayed on any database
	// that already went through it.
	if err := withoutForeignKeys(db, func() error { return migrateToDefaultTenant(db) }); err != nil {
		t.Fatalf("second migration run: %v", err)
	}

	var tenants int64
	if err := db.Model(&Tenant{}).Where("slug = ?", model.DefaultTenantSlug).Count(&tenants).Error; err != nil {
		t.Fatalf("count tenants: %v", err)
	}
	if tenants != 1 {
		t.Errorf("default tenants: got %d, want 1", tenants)
	}
}
