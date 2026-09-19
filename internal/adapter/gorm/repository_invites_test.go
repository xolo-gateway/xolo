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

	// The case an administrator types and the one an identity provider returns
	// rarely agree; both spellings must find the same invitation.
	for _, email := range []string{"Jean.Dupont@corp.tld", "jean.dupont@corp.tld", "JEAN.DUPONT@CORP.TLD"} {
		invites, err := store.ListPendingInvitesForEmail(ctx, email)
		if err != nil {
			t.Fatalf("ListPendingInvitesForEmail(%q): %v", email, err)
		}
		if len(invites) != 1 {
			t.Fatalf("ListPendingInvitesForEmail(%q): got %d invites, want 1", email, len(invites))
		}
		if invites[0].ID() != targeted.ID() {
			t.Errorf("ListPendingInvitesForEmail(%q): got invite %q, want %q", email, invites[0].ID(), targeted.ID())
		}
		// The bridge middleware scopes the invitation to the request tenant, so
		// the organization has to come back preloaded.
		if invites[0].Org() == nil || invites[0].Org().TenantID() != testTenantID {
			t.Errorf("ListPendingInvitesForEmail(%q): organization not preloaded", email)
		}
	}

	for _, email := range []string{expiredEmail, revokedEmail, "nobody@corp.tld"} {
		invites, err := store.ListPendingInvitesForEmail(ctx, email)
		if err != nil {
			t.Fatalf("ListPendingInvitesForEmail(%q): %v", email, err)
		}
		if len(invites) != 0 {
			t.Errorf("ListPendingInvitesForEmail(%q): got %d invites, want none", email, len(invites))
		}
	}
}
