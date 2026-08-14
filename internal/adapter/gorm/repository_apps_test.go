package gorm_test

import (
	"context"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
)

func TestApplicationStore_Lifecycle(t *testing.T) {
	eachBackend(t, scenarioApplicationStoreLifecycle)
}

func scenarioApplicationStoreLifecycle(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	otherOrg := model.NewOrganization(testTenantID, "other", "Other", "")
	if err := store.CreateOrg(ctx, otherOrg); err != nil {
		t.Fatalf("CreateOrg (other): %v", err)
	}

	app := model.NewApplication(org.ID(), "CI pipeline", "Runs nightly", true)
	if err := store.CreateApplication(ctx, app); err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}
	if err := store.CreateApplication(ctx, model.NewApplication(otherOrg.ID(), "Other", "", true)); err != nil {
		t.Fatalf("CreateApplication (other org): %v", err)
	}

	loaded, err := store.GetApplication(ctx, app.ID())
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if loaded.Name() != "CI pipeline" || loaded.Description() != "Runs nightly" || !loaded.Active() {
		t.Errorf("unexpected application round-trip: %+v", loaded)
	}
	if _, err := store.GetApplication(ctx, model.NewApplicationID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetApplication (unknown): expected port.ErrNotFound, got %v", err)
	}

	apps, err := store.QueryApplications(ctx, org.ID())
	if err != nil {
		t.Fatalf("QueryApplications: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected 1 application scoped to the org, got %d", len(apps))
	}

	updated := model.UpdateApplication(loaded, model.WithApplicationName("CI"))
	if err := store.UpdateApplication(ctx, updated); err != nil {
		t.Fatalf("UpdateApplication: %v", err)
	}
	loaded, err = store.GetApplication(ctx, app.ID())
	if err != nil {
		t.Fatalf("GetApplication (after update): %v", err)
	}
	if loaded.Name() != "CI" {
		t.Errorf("expected the name to be updated, got %q", loaded.Name())
	}

	if err := store.DeleteApplication(ctx, app.ID()); err != nil {
		t.Fatalf("DeleteApplication: %v", err)
	}
	if _, err := store.GetApplication(ctx, app.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetApplication (deleted): expected port.ErrNotFound, got %v", err)
	}
}

func TestApplicationStore_AuthTokens(t *testing.T) {
	eachBackend(t, scenarioApplicationStoreAuthTokens)
}

func scenarioApplicationStoreAuthTokens(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
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

	found, err := store.FindApplicationAuthToken(ctx, "app-token-value")
	if err != nil {
		t.Fatalf("FindApplicationAuthToken: %v", err)
	}
	if found.ID() != token.ID() {
		t.Errorf("expected token %q, got %q", token.ID(), found.ID())
	}
	if found.Application() == nil || found.Application().ID() != app.ID() {
		t.Error("expected the owning application to be preloaded")
	}
	if _, err := store.FindApplicationAuthToken(ctx, "unknown"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("FindApplicationAuthToken (unknown): expected port.ErrNotFound, got %v", err)
	}

	byID, err := store.GetApplicationAuthToken(ctx, token.ID())
	if err != nil {
		t.Fatalf("GetApplicationAuthToken: %v", err)
	}
	if byID.Label() != "ci-token" {
		t.Errorf("expected label %q, got %q", "ci-token", byID.Label())
	}

	tokens, err := store.GetApplicationAuthTokens(ctx, app.ID())
	if err != nil {
		t.Fatalf("GetApplicationAuthTokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}

	// Deleting the application cascades to its tokens.
	if err := store.DeleteApplication(ctx, app.ID()); err != nil {
		t.Fatalf("DeleteApplication: %v", err)
	}
	if _, err := store.FindApplicationAuthToken(ctx, "app-token-value"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("FindApplicationAuthToken (after delete): expected port.ErrNotFound, got %v", err)
	}
}

func TestInviteStore_Lifecycle(t *testing.T) {
	eachBackend(t, scenarioInviteStoreLifecycle)
}

func scenarioInviteStoreLifecycle(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	author := model.NewUserID()
	expiresAt := time.Now().Add(24 * time.Hour)

	targeted := model.NewInviteToken(org.ID(), model.RoleMember, ptr("invitee@example.com"), &expiresAt, ptr(3), author)
	if err := store.CreateInvite(ctx, targeted); err != nil {
		t.Fatalf("CreateInvite (targeted): %v", err)
	}
	open := model.NewInviteToken(org.ID(), model.RoleMember, nil, nil, nil, author)
	if err := store.CreateInvite(ctx, open); err != nil {
		t.Fatalf("CreateInvite (open): %v", err)
	}

	loaded, err := store.GetInviteByID(ctx, targeted.ID())
	if err != nil {
		t.Fatalf("GetInviteByID: %v", err)
	}
	if loaded.InviteeEmail() == nil || *loaded.InviteeEmail() != "invitee@example.com" {
		t.Errorf("expected the invitee email to round-trip, got %v", loaded.InviteeEmail())
	}
	if loaded.MaxUses() == nil || *loaded.MaxUses() != 3 {
		t.Errorf("expected max uses 3, got %v", loaded.MaxUses())
	}
	if loaded.ExpiresAt() == nil {
		t.Error("expected an expiry date")
	}
	if _, err := store.GetInviteByID(ctx, model.NewInviteTokenID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetInviteByID (unknown): expected port.ErrNotFound, got %v", err)
	}

	invites, err := store.ListInvites(ctx, org.ID())
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(invites) != 2 {
		t.Errorf("expected 2 invites, got %d", len(invites))
	}

	pending, err := store.ListPendingInvitesForEmail(ctx, "invitee@example.com")
	if err != nil {
		t.Fatalf("ListPendingInvitesForEmail: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending invite, got %d", len(pending))
	}

	if err := store.IncrementInviteUses(ctx, targeted.ID()); err != nil {
		t.Fatalf("IncrementInviteUses: %v", err)
	}
	loaded, err = store.GetInviteByID(ctx, targeted.ID())
	if err != nil {
		t.Fatalf("GetInviteByID (after increment): %v", err)
	}
	if loaded.UsesCount() != 1 {
		t.Errorf("expected 1 use, got %d", loaded.UsesCount())
	}

	// A revoked invite is no longer pending.
	if err := store.RevokeInvite(ctx, targeted.ID()); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	loaded, err = store.GetInviteByID(ctx, targeted.ID())
	if err != nil {
		t.Fatalf("GetInviteByID (after revoke): %v", err)
	}
	if loaded.RevokedAt() == nil {
		t.Error("expected the invite to carry a revocation date")
	}
	pending, err = store.ListPendingInvitesForEmail(ctx, "invitee@example.com")
	if err != nil {
		t.Fatalf("ListPendingInvitesForEmail (after revoke): %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected no pending invite after revocation, got %d", len(pending))
	}

	if err := store.DeleteInvite(ctx, targeted.ID()); err != nil {
		t.Fatalf("DeleteInvite: %v", err)
	}
	if _, err := store.GetInviteByID(ctx, targeted.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetInviteByID (deleted): expected port.ErrNotFound, got %v", err)
	}
}

// TestInviteStore_ExpiredNotPending checks that an invite past its expiry date
// stops being offered, a filter evaluated in SQL against the current time.
func TestInviteStore_ExpiredNotPending(t *testing.T) {
	eachBackend(t, scenarioInviteStoreExpiredNotPending)
}

func scenarioInviteStoreExpiredNotPending(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	expired := time.Now().Add(-time.Hour)
	invite := model.NewInviteToken(org.ID(), model.RoleMember, ptr("late@example.com"), &expired, nil, model.NewUserID())
	if err := store.CreateInvite(ctx, invite); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	pending, err := store.ListPendingInvitesForEmail(ctx, "late@example.com")
	if err != nil {
		t.Fatalf("ListPendingInvitesForEmail: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("expected an expired invite not to be pending, got %d", len(pending))
	}
}

// testTenantID is the tenant every fixture of this package belongs to.
// Tenancy is not what these tests exercise: they only need a stable, shared
// owner so the tenant-scoped unique keys behave like the pre-tenant ones.
const testTenantID = model.TenantID("test-tenant")
