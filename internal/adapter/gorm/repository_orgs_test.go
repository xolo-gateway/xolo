package gorm_test

import (
	"context"
	"testing"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
)

func TestOrgStore_Lifecycle(t *testing.T) {
	eachBackend(t, scenarioOrgStoreLifecycle)
}

func scenarioOrgStoreLifecycle(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme Corp", "The description", "EUR")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	byID, err := store.GetOrgByID(ctx, org.ID())
	if err != nil {
		t.Fatalf("GetOrgByID: %v", err)
	}
	if byID.Name() != "Acme Corp" || byID.Currency() != "EUR" || !byID.Active() {
		t.Errorf("unexpected org round-trip: name=%q currency=%q active=%v", byID.Name(), byID.Currency(), byID.Active())
	}

	bySlug, err := store.GetOrgBySlug(ctx, testTenantID, "acme")
	if err != nil {
		t.Fatalf("GetOrgBySlug: %v", err)
	}
	if bySlug.ID() != org.ID() {
		t.Errorf("expected org %q, got %q", org.ID(), bySlug.ID())
	}

	if _, err := store.GetOrgBySlug(ctx, testTenantID, "unknown"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetOrgBySlug (unknown): expected port.ErrNotFound, got %v", err)
	}
	if _, err := store.GetOrgByID(ctx, model.NewOrgID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetOrgByID (unknown): expected port.ErrNotFound, got %v", err)
	}

	updated := model.UpdateOrganization(byID,
		model.WithOrgName("Acme SAS"),
		model.WithOrgActive(false),
		model.WithOrgShareQuotaEqually(true),
	)
	if err := store.SaveOrg(ctx, updated); err != nil {
		t.Fatalf("SaveOrg: %v", err)
	}
	byID, err = store.GetOrgByID(ctx, org.ID())
	if err != nil {
		t.Fatalf("GetOrgByID (after save): %v", err)
	}
	if byID.Name() != "Acme SAS" || byID.Active() || !byID.ShareQuotaEqually() {
		t.Errorf("unexpected org after save: name=%q active=%v shareQuota=%v",
			byID.Name(), byID.Active(), byID.ShareQuotaEqually())
	}

	if err := store.DeleteOrg(ctx, org.ID()); err != nil {
		t.Fatalf("DeleteOrg: %v", err)
	}
	if _, err := store.GetOrgByID(ctx, org.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetOrgByID (deleted): expected port.ErrNotFound, got %v", err)
	}
}

func TestOrgStore_DeletePurgesScopedData(t *testing.T) {
	eachBackend(t, scenarioOrgStoreDeletePurgesScopedData)
}

// Deleting a tenant must leave nothing behind, in particular no application and
// no auth token that would still resolve after the organization is gone.
func scenarioOrgStoreDeletePurgesScopedData(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme Corp", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	app := model.NewApplication(org.ID(), "CI", "", true)
	if err := store.CreateApplication(ctx, app); err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}

	token := model.NewApplicationAuthToken(app, org.ID(), "ci-token", "app-token-value", nil)
	if err := store.CreateApplicationAuthToken(ctx, token); err != nil {
		t.Fatalf("CreateApplicationAuthToken: %v", err)
	}

	if err := store.EnsureBuiltinRoles(ctx, org.ID()); err != nil {
		t.Fatalf("EnsureBuiltinRoles: %v", err)
	}

	if err := store.DeleteOrg(ctx, org.ID()); err != nil {
		t.Fatalf("DeleteOrg: %v", err)
	}

	if _, err := store.GetApplication(ctx, app.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("GetApplication (after org delete): expected port.ErrNotFound, got %v", err)
	}
	if _, err := store.FindApplicationAuthToken(ctx, "app-token-value"); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("FindApplicationAuthToken (after org delete): expected port.ErrNotFound, got %v", err)
	}

	roles, err := store.ListOrgRoles(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListOrgRoles (after org delete): %v", err)
	}
	if len(roles) != 0 {
		t.Errorf("expected the org roles to be purged, got %d", len(roles))
	}
}

func TestOrgStore_ListPagination(t *testing.T) {
	eachBackend(t, scenarioOrgStoreListPagination)
}

func scenarioOrgStoreListPagination(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	for _, slug := range []string{"org-a", "org-b", "org-c"} {
		if err := store.CreateOrg(ctx, model.NewOrganization(testTenantID, slug, slug, "")); err != nil {
			t.Fatalf("CreateOrg(%s): %v", slug, err)
		}
	}

	orgs, total, err := store.ListOrgs(ctx, port.ListOrgsOptions{Page: ptr(0), Limit: ptr(2)})
	if err != nil {
		t.Fatalf("ListOrgs: %v", err)
	}
	if total != 3 {
		t.Errorf("expected a total of 3 orgs, got %d", total)
	}
	if len(orgs) != 2 {
		t.Errorf("expected 2 orgs on the first page, got %d", len(orgs))
	}

	orgs, _, err = store.ListOrgs(ctx, port.ListOrgsOptions{Page: ptr(1), Limit: ptr(2)})
	if err != nil {
		t.Fatalf("ListOrgs (page 1): %v", err)
	}
	if len(orgs) != 1 {
		t.Errorf("expected 1 org on the second page, got %d", len(orgs))
	}
}

func TestOrgStore_Memberships(t *testing.T) {
	eachBackend(t, scenarioOrgStoreMemberships)
}

func scenarioOrgStoreMemberships(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	otherOrg := model.NewOrganization(testTenantID, "other", "Other", "")
	if err := store.CreateOrg(ctx, otherOrg); err != nil {
		t.Fatalf("CreateOrg (other): %v", err)
	}

	member := newUser("s-member", "member@example.com", "Member", true)
	if err := store.SaveUser(ctx, member); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}
	outsider := newUser("s-outsider", "outsider@example.com", "Outsider", true)
	if err := store.SaveUser(ctx, outsider); err != nil {
		t.Fatalf("SaveUser (outsider): %v", err)
	}

	membership := model.NewMembership(member.ID(), org.ID())
	if err := store.AddMember(ctx, membership); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if err := store.AddMember(ctx, model.NewMembership(member.ID(), otherOrg.ID())); err != nil {
		t.Fatalf("AddMember (other org): %v", err)
	}

	isMember, err := store.IsMember(ctx, member.ID(), org.ID())
	if err != nil {
		t.Fatalf("IsMember: %v", err)
	}
	if !isMember {
		t.Error("expected the user to be a member")
	}
	isMember, err = store.IsMember(ctx, outsider.ID(), org.ID())
	if err != nil {
		t.Fatalf("IsMember (outsider): %v", err)
	}
	if isMember {
		t.Error("expected the outsider not to be a member")
	}

	found, err := store.GetUserOrgMembership(ctx, member.ID(), org.ID())
	if err != nil {
		t.Fatalf("GetUserOrgMembership: %v", err)
	}
	if found.ID() != membership.ID() {
		t.Errorf("expected membership %q, got %q", membership.ID(), found.ID())
	}
	if _, err := store.GetUserOrgMembership(ctx, outsider.ID(), org.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetUserOrgMembership (outsider): expected port.ErrNotFound, got %v", err)
	}

	memberships, err := store.GetUserMemberships(ctx, member.ID())
	if err != nil {
		t.Fatalf("GetUserMemberships: %v", err)
	}
	if len(memberships) != 2 {
		t.Errorf("expected the user to belong to 2 orgs, got %d", len(memberships))
	}

	members, total, err := store.ListOrgMembers(ctx, org.ID(), port.ListOrgMembersOptions{})
	if err != nil {
		t.Fatalf("ListOrgMembers: %v", err)
	}
	if total != 1 || len(members) != 1 {
		t.Errorf("expected 1 member, got %d (total %d)", len(members), total)
	}
	if members[0].User() == nil || members[0].User().ID() != member.ID() {
		t.Error("expected the member's user to be preloaded")
	}

	if err := store.RemoveMember(ctx, membership.ID()); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if _, err := store.GetMembership(ctx, membership.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetMembership (removed): expected port.ErrNotFound, got %v", err)
	}

	// Deleting an org cascades to the memberships it still holds.
	if err := store.DeleteOrg(ctx, otherOrg.ID()); err != nil {
		t.Fatalf("DeleteOrg: %v", err)
	}
	memberships, err = store.GetUserMemberships(ctx, member.ID())
	if err != nil {
		t.Fatalf("GetUserMemberships (after org delete): %v", err)
	}
	if len(memberships) != 0 {
		t.Errorf("expected memberships to be cascaded away, got %d", len(memberships))
	}
}
