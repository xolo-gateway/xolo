package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/http/handler/api"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// emptyPermissionResolver stands in for the resolver installed by the
// memberships middleware, resolving every org to an empty permission set so the
// cross-org access test denies the attacker.
func emptyPermissionResolver(ctx context.Context, orgID model.OrgID) (rbac.PermissionSet, error) {
	return rbac.NewPermissionSet(nil, nil), nil
}

// fakeOrgStore embeds the interface (nil) so unused methods panic loudly if
// ever called by the code under test, while letting us override just the
// ones exercised by hasOrgAdminAccess.
type fakeOrgStore struct {
	port.OrgStore
	orgsBySlug  map[string]model.Organization
	memberships map[string]model.Membership // key: userID+"/"+orgID
}

func (s *fakeOrgStore) GetOrgBySlug(ctx context.Context, tenantID model.TenantID, slug string) (model.Organization, error) {
	org, ok := s.orgsBySlug[slug]
	if !ok {
		return nil, port.ErrNotFound
	}
	if org.TenantID() != tenantID {
		return nil, port.ErrNotFound
	}
	return org, nil
}

func (s *fakeOrgStore) GetUserOrgMembership(ctx context.Context, userID model.UserID, orgID model.OrgID) (model.Membership, error) {
	m, ok := s.memberships[string(userID)+"/"+string(orgID)]
	if !ok {
		return nil, port.ErrNotFound
	}
	return m, nil
}

// fakeVirtualModelStore embeds the interface (nil); only GetVirtualModelByID
// and DeleteVirtualModel are exercised by the cross-org deletion test.
type fakeVirtualModelStore struct {
	port.VirtualModelStore
	byID    map[model.VirtualModelID]model.VirtualModel
	deleted []model.VirtualModelID
}

func (s *fakeVirtualModelStore) GetVirtualModelByID(ctx context.Context, id model.VirtualModelID) (model.VirtualModel, error) {
	vm, ok := s.byID[id]
	if !ok {
		return nil, port.ErrNotFound
	}
	return vm, nil
}

func (s *fakeVirtualModelStore) DeleteVirtualModel(ctx context.Context, id model.VirtualModelID) error {
	s.deleted = append(s.deleted, id)
	return nil
}

// TestHandleDeleteVirtualModel_RejectsCrossOrgRequest is a regression test
// for a confirmed authz bug: handleDeleteVirtualModel resolved the virtual
// model purely by vmID and never checked that the authenticated user
// belonged to the model's owning org, so any authenticated user could
// forge a DELETE for a vmID belonging to an org they have no access to.
func TestHandleDeleteVirtualModel_RejectsCrossOrgRequest(t *testing.T) {
	orgA := model.NewOrganization(testTenantID, "org-a", "Org A", "")
	orgB := model.NewOrganization(testTenantID, "org-b", "Org B", "")

	attacker := model.NewUser(testTenantID, "test", "attacker", "attacker@example.com", "Attacker", true, "user")

	orgStore := &fakeOrgStore{
		orgsBySlug: map[string]model.Organization{
			"org-a": orgA,
			"org-b": orgB,
		},
		memberships: map[string]model.Membership{
			// Attacker is only a member of org A, not org B.
			string(attacker.ID()) + "/" + string(orgA.ID()): model.NewMembership(attacker.ID(), orgA.ID()),
		},
	}

	victimVM := model.NewVirtualModel(orgB.ID(), "victim", "")
	vmStore := &fakeVirtualModelStore{
		byID: map[model.VirtualModelID]model.VirtualModel{
			victimVM.ID(): victimVM,
		},
	}

	h := api.NewHandler(nil, orgStore, vmStore, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/orgs/org-a/virtual-models/"+string(victimVM.ID()), nil)
	req.SetPathValue("orgSlug", "org-a")
	req.SetPathValue("vmID", string(victimVM.ID()))
	ctx := httpCtx.SetUser(req.Context(), attacker)
	ctx = httpCtx.SetPermissionResolver(ctx, emptyPermissionResolver)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 (no leak of cross-org existence), got %d: %s", rec.Code, rec.Body.String())
	}
	if len(vmStore.deleted) != 0 {
		t.Fatalf("expected DeleteVirtualModel to never be called, but it was called with %v", vmStore.deleted)
	}
}

var _ http.Handler = &api.Handler{}

// testTenantID is the tenant every fixture of this package belongs to.
// Tenancy is not what these tests exercise: they only need a stable, shared
// owner so the tenant-scoped unique keys behave like the pre-tenant ones.
const testTenantID = model.TenantID("test-tenant")
