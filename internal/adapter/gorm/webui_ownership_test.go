package gorm_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/admin"
	"github.com/xolo-gateway/xolo/internal/http/middleware/memberships"
	gormpkg "gorm.io/gorm"
)

// TestWebUIOwnership refuses local writes to control-plane families with
// 403, for full pages and htmx fragments alike, and changes nothing.
func TestWebUIOwnership(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		tenant, err := base.GetTenantByID(ctx, testTenantID)
		require.NoError(t, err)
		operator := model.NewUser(testTenantID, "oidc", "operator", "operator@example.test", "Operator", true, model.PlatformRoleUser, model.PlatformRoleAdmin)
		require.NoError(t, base.SaveUser(ctx, operator))

		org, err := base.GetOrgByID(ctx, fixture.org)
		require.NoError(t, err)
		roles, err := base.ListOrgRoles(ctx, fixture.org)
		require.NoError(t, err)
		require.NotEmpty(t, roles)

		store := ownedStore(t, db, model.OwnershipPolicy{
			model.FamilyMember:                 model.OwnerControlPlane,
			model.FamilyOrganization:           model.OwnerControlPlane,
			model.FamilyOrganizationMembership: model.OwnerControlPlane,
		})
		adminHandler := admin.NewHandler(store, store, store, store, nil, nil, nil)
		orgHandler := memberships.Middleware(store, store)(webui.NewHandler(
			nil, store, store, store, store, store, store, store, store, store, service.NewInvitationService(store), store, store, nil, nil, store, scopeTestSecretKey,
			nil, nil, nil, store, store, store, store, 100, 100,
		))
		for _, fragment := range []bool{false, true} {
			for _, op := range []struct {
				handler            http.Handler
				method, path, body string
			}{
				{adminHandler, http.MethodDelete, "/users/" + string(fixture.user), ""},
				{adminHandler, http.MethodPost, "/orgs/" + string(fixture.org) + "/edit", "name=Changed&active=on"},
				{orgHandler, http.MethodPost, "/orgs/" + org.Slug() + "/admin/invites", "role=" + string(roles[0].ID())},
			} {
				r := httptest.NewRequest(op.method, op.path, strings.NewReader(op.body))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if fragment {
					r.Header.Set("HX-Request", "true")
				}
				rctx := httpCtx.SetTenant(r.Context(), tenant)
				rctx = httpCtx.SetUser(rctx, operator)
				rctx = httpCtx.SetBaseURL(rctx, "https://gateway.test")
				w := httptest.NewRecorder()
				op.handler.ServeHTTP(w, r.WithContext(rctx))
				require.Equal(t, http.StatusForbidden, w.Code, "%s %s", op.method, op.path)
				require.Contains(t, w.Body.String(), "système de pilotage", "the ownership page, not the admin guard")
			}
		}
		_, err = base.GetUserByID(ctx, fixture.user)
		require.NoError(t, err)
		org, err = base.GetOrgByID(ctx, fixture.org)
		require.NoError(t, err)
		require.Equal(t, "Org", org.Name())
		invites, err := base.ListInvites(ctx, fixture.org)
		require.NoError(t, err)
		require.Empty(t, invites)
	})
}
