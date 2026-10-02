package gorm_test

import (
	"context"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

func TestInviteStore_ListPendingInvitesForEmail(t *testing.T) {
	eachBackend(t, scenarioListPendingInvitesForEmail)
}

func scenarioListPendingInvitesForEmail(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	org := model.NewOrganization(testTenantID, "acme", "Acme Corp", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	past := time.Now().Add(-time.Hour)

	create := func(email *string, expiresAt *time.Time) model.InviteToken {
		t.Helper()
		invite := model.NewInviteToken(org.ID(), model.RoleMember, email, expiresAt, nil, model.NewUserID())
		if err := store.CreateInvite(ctx, invite); err != nil {
			t.Fatalf("CreateInvite: %v", err)
		}
		return invite
	}

	addressed := "Jean.Dupont@corp.tld"
	targeted := create(&addressed, nil)

	expiredEmail := "expired@corp.tld"
	create(&expiredEmail, &past)

	revokedEmail := "revoked@corp.tld"
	revoked := create(&revokedEmail, nil)
	if err := store.RevokeInvite(ctx, revoked.ID()); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}

	create(nil, nil) // open link, addressed to nobody

	// Same addressee, but issued by an organization of another tenant.
	otherTenant := model.TenantID("other-tenant")
	foreignOrg := model.NewOrganization(otherTenant, "acme", "Acme Elsewhere", "")
	if err := store.CreateOrg(ctx, foreignOrg); err != nil {
		t.Fatalf("CreateOrg (other tenant): %v", err)
	}
	foreign := model.NewInviteToken(foreignOrg.ID(), model.RoleMember, &addressed, nil, nil, model.NewUserID())
	if err := store.CreateInvite(ctx, foreign); err != nil {
		t.Fatalf("CreateInvite (other tenant): %v", err)
	}

	// The case an administrator types and the one an identity provider returns
	// rarely agree; every spelling must find the same invitation, and only the
	// one of the requested tenant.
	for _, email := range []string{"Jean.Dupont@corp.tld", "jean.dupont@corp.tld", "JEAN.DUPONT@CORP.TLD", " jean.dupont@corp.tld "} {
		invites, err := store.ListPendingInvitesForEmail(ctx, testTenantID, email)
		if err != nil {
			t.Fatalf("ListPendingInvitesForEmail(%q): %v", email, err)
		}
		if len(invites) != 1 {
			t.Fatalf("ListPendingInvitesForEmail(%q): got %d invites, want 1", email, len(invites))
		}
		if invites[0].ID() != targeted.ID() {
			t.Errorf("ListPendingInvitesForEmail(%q): got invite %q, want %q", email, invites[0].ID(), targeted.ID())
		}
		// /no-org and the profile page display the organization.
		if invites[0].Org() == nil || invites[0].Org().TenantID() != testTenantID {
			t.Errorf("ListPendingInvitesForEmail(%q): organization not preloaded", email)
		}
	}

	// Stored normalized, which is what keeps the invitee_email index usable.
	stored, err := store.GetInviteByID(ctx, targeted.ID())
	if err != nil {
		t.Fatalf("GetInviteByID: %v", err)
	}
	if got := *stored.InviteeEmail(); got != "jean.dupont@corp.tld" {
		t.Errorf("invitee e-mail stored as %q, want it normalized", got)
	}

	foreignInvites, err := store.ListPendingInvitesForEmail(ctx, otherTenant, addressed)
	if err != nil {
		t.Fatalf("ListPendingInvitesForEmail (other tenant): %v", err)
	}
	if len(foreignInvites) != 1 || foreignInvites[0].ID() != foreign.ID() {
		t.Errorf("ListPendingInvitesForEmail (other tenant): got %d invites, want only %q", len(foreignInvites), foreign.ID())
	}

	for _, email := range []string{expiredEmail, revokedEmail, "nobody@corp.tld"} {
		invites, err := store.ListPendingInvitesForEmail(ctx, testTenantID, email)
		if err != nil {
			t.Fatalf("ListPendingInvitesForEmail(%q): %v", email, err)
		}
		if len(invites) != 0 {
			t.Errorf("ListPendingInvitesForEmail(%q): got %d invites, want none", email, len(invites))
		}
	}
}
