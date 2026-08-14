package gorm

import (
	"testing"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// TestMigrateLegacyMembershipRoles exercises the upgrade path on an existing
// database: legacy memberships carrying a single Role string must be converted
// into builtin role assignments.
func TestMigrateLegacyMembershipRoles(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	// Legacy schema: organizations + memberships with a "role" column.
	if err := db.AutoMigrate(&Organization{}); err != nil {
		t.Fatalf("migrate org: %v", err)
	}
	type legacyMembershipTable struct {
		ID     string `gorm:"primaryKey"`
		UserID string
		OrgID  string
		Role   string
	}
	if err := db.Table("memberships").AutoMigrate(&legacyMembershipTable{}); err != nil {
		t.Fatalf("migrate legacy memberships: %v", err)
	}

	org := fromOrganization(model.NewOrganization(testTenantID, "acme", "Acme", ""))
	if err := db.Create(org).Error; err != nil {
		t.Fatalf("create org: %v", err)
	}

	rows := []legacyMembershipTable{
		{ID: "m-owner", UserID: "u1", OrgID: org.ID, Role: model.RoleOrgOwner},
		{ID: "m-admin", UserID: "u2", OrgID: org.ID, Role: model.RoleOrgAdmin},
		{ID: "m-member", UserID: "u3", OrgID: org.ID, Role: model.RoleMember},
	}
	for _, r := range rows {
		if err := db.Table("memberships").Create(&r).Error; err != nil {
			t.Fatalf("create legacy membership: %v", err)
		}
	}

	// Run the migration body (idempotently, twice). gormigrate runs migrations
	// outside a transaction, so PRAGMA foreign_keys=off takes effect — mirror that.
	runMigration := func() error {
		if err := db.Exec("PRAGMA foreign_keys=off").Error; err != nil {
			return err
		}
		if err := db.SetupJoinTable(&Membership{}, "Roles", &MembershipRole{}); err != nil {
			return err
		}
		if err := db.AutoMigrate(&Role{}, &RolePermission{}, &RoleModel{}, &MembershipRole{}); err != nil {
			return err
		}
		if err := migrateLegacyMembershipRoles(db); err != nil {
			return err
		}
		if db.Migrator().HasColumn(&Membership{}, "role") {
			if err := db.Migrator().DropColumn(&Membership{}, "role"); err != nil {
				return err
			}
		}
		return db.Exec("PRAGMA foreign_keys=on").Error
	}
	for range 2 {
		if err := runMigration(); err != nil {
			t.Fatalf("migration: %v", err)
		}
	}

	// Builtin roles created.
	var roleCount int64
	db.Model(&Role{}).Where("org_id = ?", org.ID).Count(&roleCount)
	if roleCount != 3 {
		t.Fatalf("expected 3 builtin roles, got %d", roleCount)
	}

	// Each legacy membership got exactly one role assignment.
	var assignCount int64
	db.Model(&MembershipRole{}).Count(&assignCount)
	if assignCount != 3 {
		t.Fatalf("expected 3 membership role assignments, got %d", assignCount)
	}

	// The owner membership maps to the owner builtin role.
	var ownerRoleID string
	db.Model(&Role{}).Where("org_id = ? AND builtin_kind = ?", org.ID, model.BuiltinKindOwner).Pluck("id", &ownerRoleID)
	var got int64
	db.Model(&MembershipRole{}).Where("membership_id = ? AND role_id = ?", "m-owner", ownerRoleID).Count(&got)
	if got != 1 {
		t.Fatalf("owner membership not mapped to owner role")
	}
}

// TestBackfillApplicationBuiltinRoles exercises the upgrade path for
// applications created before they could hold roles: each must end up with the
// builtin member role, or it stays locked out of the proxy.
func TestBackfillApplicationBuiltinRoles(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	if err := db.AutoMigrate(&Organization{}, &Application{}, &Role{}, &RolePermission{}, &RoleModel{}, &ApplicationRole{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	org := fromOrganization(model.NewOrganization(testTenantID, "acme", "Acme", ""))
	if err := db.Create(org).Error; err != nil {
		t.Fatalf("create org: %v", err)
	}

	other := fromOrganization(model.NewOrganization(testTenantID, "other", "Other", ""))
	if err := db.Create(other).Error; err != nil {
		t.Fatalf("create other org: %v", err)
	}

	// Builtin roles for both orgs.
	roleIDByOrg := map[string]string{}
	for _, o := range []*Organization{org, other} {
		for _, spec := range builtinRoleSpecs() {
			role := fromRole(model.NewBuiltinRole(model.OrgID(o.ID), spec.kind, spec.name, spec.description, spec.permissions))
			if err := db.Omit("Org").Create(role).Error; err != nil {
				t.Fatalf("create builtin role: %v", err)
			}
			if spec.kind == model.BuiltinKindMember {
				roleIDByOrg[o.ID] = role.ID
			}
		}
	}

	apps := []*Application{
		{ID: "app-1", OrgID: org.ID, Name: "CI", Active: true},
		{ID: "app-2", OrgID: org.ID, Name: "Bot", Active: true},
		{ID: "app-3", OrgID: other.ID, Name: "Other CI", Active: true},
	}
	for _, a := range apps {
		if err := db.Create(a).Error; err != nil {
			t.Fatalf("create application: %v", err)
		}
	}

	// An application that already holds a role must be left untouched.
	ownerRoleID := ""
	db.Model(&Role{}).Where("org_id = ? AND builtin_kind = ?", org.ID, model.BuiltinKindOwner).Pluck("id", &ownerRoleID)
	if err := db.Create(&ApplicationRole{ApplicationID: "app-2", RoleID: ownerRoleID}).Error; err != nil {
		t.Fatalf("create existing assignment: %v", err)
	}

	// Idempotent: running it twice must not duplicate assignments.
	for range 2 {
		if err := backfillApplicationBuiltinRoles(db); err != nil {
			t.Fatalf("backfill: %v", err)
		}
	}

	assertAssignment := func(appID, roleID string) {
		t.Helper()
		var count int64
		db.Model(&ApplicationRole{}).Where("application_id = ?", appID).Count(&count)
		if count != 1 {
			t.Fatalf("expected exactly 1 assignment for %s, got %d", appID, count)
		}
		var got string
		db.Model(&ApplicationRole{}).Where("application_id = ?", appID).Pluck("role_id", &got)
		if got != roleID {
			t.Errorf("%s: expected role %q, got %q", appID, roleID, got)
		}
	}

	assertAssignment("app-1", roleIDByOrg[org.ID])
	// Untouched: kept its pre-existing owner role instead of gaining member.
	assertAssignment("app-2", ownerRoleID)
	// Scoped per org: never assigned another org's member role.
	assertAssignment("app-3", roleIDByOrg[other.ID])
}

// testTenantID is the tenant every fixture of this package belongs to.
// Tenancy is not what these tests exercise: they only need a stable, shared
// owner so the tenant-scoped unique keys behave like the pre-tenant ones.
const testTenantID = model.TenantID("test-tenant")
