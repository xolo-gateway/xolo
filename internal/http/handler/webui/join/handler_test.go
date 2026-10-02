package join_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/pkg/errors"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/join"
	gormpkg "gorm.io/gorm"
)

func TestJoinHandler(t *testing.T) {
	ctx := context.Background()

	tenant := model.NewTenant("acme", "Acme", "")

	setup := func(t *testing.T, orgTenant model.TenantID, invitee string) (*xologorm.Store, model.InviteToken) {
		t.Helper()

		db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		store := xologorm.NewStore(db)

		org := model.NewOrganization(orgTenant, "acme", "Acme", "")
		if err := store.CreateOrg(ctx, org); err != nil {
			t.Fatalf("create org: %v", err)
		}
		invite := model.NewInviteToken(org.ID(), model.RoleMember, &invitee, nil, nil, model.NewUserID())
		if err := store.CreateInvite(ctx, invite); err != nil {
			t.Fatalf("create invite: %v", err)
		}

		return store, invite
	}

	post := func(t *testing.T, store *xologorm.Store, path string, user model.User) int {
		t.Helper()

		req := httptest.NewRequest(http.MethodPost, path, nil)
		reqCtx := httpCtx.SetBaseURL(req.Context(), "/")
		reqCtx = httpCtx.SetCurrentURL(reqCtx, req.URL)
		reqCtx = httpCtx.SetTenant(reqCtx, tenant)
		reqCtx = httpCtx.SetUser(reqCtx, user)

		rec := httptest.NewRecorder()
		join.NewHandler(store, store, store).ServeHTTP(rec, req.WithContext(reqCtx))

		return rec.Code
	}

	newUser := func(t *testing.T, store *xologorm.Store, email string) model.User {
		t.Helper()
		// Inactive on purpose: /join is mounted outside authz.Active().
		user := model.NewUser(tenant.ID(), "openid-connect", "sub-"+email, email, "Jean", false, model.PlatformRoleUser)
		if err := store.SaveUser(ctx, user); err != nil {
			t.Fatalf("save user: %v", err)
		}
		return user
	}

	// Invitation IDs are globally unique and travel in the clear: one issued by
	// another tenant must not hand out a membership here.
	t.Run("refuses an invitation of another tenant", func(t *testing.T) {
		store, invite := setup(t, model.TenantID("other-tenant"), "jean@corp.tld")
		user := newUser(t, store, "jean@corp.tld")

		post(t, store, "/"+string(invite.ID()), user)

		if member, err := store.IsMember(ctx, user.ID(), invite.OrgID()); err != nil || member {
			t.Errorf("membership granted across tenants (member=%v, err=%v)", member, err)
		}
	})

	t.Run("lets an inactive invitee accept", func(t *testing.T) {
		store, invite := setup(t, tenant.ID(), "jean@corp.tld")
		user := newUser(t, store, "Jean@corp.tld")

		post(t, store, "/"+string(invite.ID()), user)

		if member, err := store.IsMember(ctx, user.ID(), invite.OrgID()); err != nil || !member {
			t.Errorf("invitee should have joined (member=%v, err=%v)", member, err)
		}
	})

	// Declining deletes a targeted invitation: only its addressee may do that.
	t.Run("refuses a decline from someone else", func(t *testing.T) {
		store, invite := setup(t, tenant.ID(), "jean@corp.tld")
		intruder := newUser(t, store, "mallory@corp.tld")

		post(t, store, "/"+string(invite.ID())+"/decline", intruder)

		if _, err := store.GetInviteByID(ctx, invite.ID()); err != nil {
			t.Errorf("invitation should have survived, got %v", err)
		}
	})

	t.Run("lets an inactive invitee decline", func(t *testing.T) {
		store, invite := setup(t, tenant.ID(), "jean@corp.tld")
		user := newUser(t, store, "jean@corp.tld")

		if status := post(t, store, "/"+string(invite.ID())+"/decline", user); status != http.StatusSeeOther {
			t.Errorf("status: got %d, want %d", status, http.StatusSeeOther)
		}

		if _, err := store.GetInviteByID(ctx, invite.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("invitation should have been deleted, got %v", err)
		}
	})
}
