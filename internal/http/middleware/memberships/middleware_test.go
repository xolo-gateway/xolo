package memberships

import (
	"context"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
)

// stubRoleStore records which resolution path was taken and returns a fixed
// permission set for each one.
type stubRoleStore struct {
	port.RoleStore

	userCalls int
	appCalls  int
	appID     model.ApplicationID
	orgID     model.OrgID
}

func (s *stubRoleStore) ResolveEffectivePermissions(_ context.Context, _ model.UserID, orgID model.OrgID) (rbac.PermissionSet, error) {
	s.userCalls++
	s.orgID = orgID
	return rbac.NewPermissionSet([]string{string(rbac.PermUsageRead)}, nil), nil
}

func (s *stubRoleStore) ResolveApplicationPermissions(_ context.Context, appID model.ApplicationID, orgID model.OrgID) (rbac.PermissionSet, error) {
	s.appCalls++
	s.appID = appID
	s.orgID = orgID
	return rbac.NewPermissionSet([]string{string(rbac.PermModelUseOrg)}, nil), nil
}

func TestPermissionResolver_Application(t *testing.T) {
	store := &stubRoleStore{}
	// The shadow user backing an application: provider "application", subject
	// carrying the ApplicationID.
	user := model.NewUser(testTenantID, model.ApplicationProvider, "app-123", "", "CI", true, authz.RoleUser)

	resolve := newPermissionResolver(store, user)

	set, err := resolve(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if store.appCalls != 1 {
		t.Errorf("expected application resolution, got %d call(s)", store.appCalls)
	}
	if store.userCalls != 0 {
		t.Errorf("expected no membership resolution, got %d call(s)", store.userCalls)
	}
	if store.appID != "app-123" {
		t.Errorf("expected the subject to be used as ApplicationID, got %q", store.appID)
	}
	if !set.Has(rbac.PermModelUseOrg) {
		t.Error("expected the application permission set to be returned")
	}

	// The set is memoized per org.
	if _, err := resolve(context.Background(), "org-1"); err != nil {
		t.Fatalf("resolve again: %v", err)
	}
	if store.appCalls != 1 {
		t.Errorf("expected the result to be memoized, got %d call(s)", store.appCalls)
	}
}

// The platform admin role must never leak to an application: its shadow user is
// created by the authn bridge, not granted by an operator.
func TestPermissionResolver_ApplicationNeverGlobalAdmin(t *testing.T) {
	store := &stubRoleStore{}
	user := model.NewUser(testTenantID, model.ApplicationProvider, "app-123", "", "CI", true, authz.RoleAdmin)

	set, err := newPermissionResolver(store, user)(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if set.IsOwner() {
		t.Error("an application must not inherit the global admin bypass")
	}
	if store.appCalls != 1 {
		t.Errorf("expected application resolution, got %d call(s)", store.appCalls)
	}
}

func TestPermissionResolver_User(t *testing.T) {
	store := &stubRoleStore{}
	user := model.NewUser(testTenantID, "oidc", "u1", "u1@example.com", "U1", true, authz.RoleUser)

	set, err := newPermissionResolver(store, user)(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if store.userCalls != 1 {
		t.Errorf("expected membership resolution, got %d call(s)", store.userCalls)
	}
	if store.appCalls != 0 {
		t.Errorf("expected no application resolution, got %d call(s)", store.appCalls)
	}
	if !set.Has(rbac.PermUsageRead) {
		t.Error("expected the membership permission set to be returned")
	}
}

func TestPermissionResolver_GlobalAdmin(t *testing.T) {
	store := &stubRoleStore{}
	user := model.NewUser(testTenantID, "oidc", "u1", "u1@example.com", "U1", true, authz.RoleUser, authz.RoleAdmin)

	set, err := newPermissionResolver(store, user)(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if !set.IsOwner() {
		t.Error("a global admin should bypass org-level resolution")
	}
	if store.userCalls != 0 || store.appCalls != 0 {
		t.Error("a global admin should not hit the role store")
	}
}

// testTenantID is the tenant every fixture of this package belongs to.
// Tenancy is not what these tests exercise: they only need a stable, shared
// owner so the tenant-scoped unique keys behave like the pre-tenant ones.
const testTenantID = model.TenantID("test-tenant")
