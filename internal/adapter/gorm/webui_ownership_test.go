package gorm_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
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

		// One resource of each business family, written while still shared.
		customRole := model.NewRole(fixture.org, "Analyst", "")
		require.NoError(t, base.CreateRole(ctx, customRole))
		provider := model.NewProvider(fixture.org, "OpenAI", "openai", "https://llm.example.test/v1", "ciphertext", "EUR")
		require.NoError(t, base.CreateProvider(ctx, provider))
		app := model.NewApplication(fixture.org, "Batch", "", true)
		require.NoError(t, base.CreateApplication(ctx, app))
		alert := model.NewAlert(fixture.org, fixture.user, "Errors", model.WithAlertQuery(`{type="llm.request.failed"}`))
		require.NoError(t, base.CreateAlert(ctx, alert))

		policy := model.OwnershipPolicy{
			model.FamilyMember:                 model.OwnerControlPlane,
			model.FamilyOrganization:           model.OwnerControlPlane,
			model.FamilyOrganizationMembership: model.OwnerControlPlane,
		}
		for _, family := range model.BusinessFamilies {
			policy[family] = model.OwnerControlPlane
		}
		store := ownedStore(t, db, policy)
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
				{orgHandler, http.MethodDelete, "/orgs/" + org.Slug() + "/admin/roles/" + string(customRole.ID()), ""},
				{orgHandler, http.MethodDelete, "/orgs/" + org.Slug() + "/admin/providers/" + string(provider.ID()), ""},
				{orgHandler, http.MethodPost, "/orgs/" + org.Slug() + "/admin/applications/" + string(app.ID()) + "/delete", ""},
				{orgHandler, http.MethodPost, "/orgs/" + org.Slug() + "/admin/quota", "daily_budget=100"},
				{orgHandler, http.MethodPost, "/orgs/" + org.Slug() + "/events/alerts/" + string(alert.ID()) + "/delete", ""},
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
		_, err = base.GetRoleByID(ctx, customRole.ID())
		require.NoError(t, err)
		_, err = base.GetProviderByID(ctx, provider.ID())
		require.NoError(t, err)
		_, err = base.GetApplication(ctx, app.ID())
		require.NoError(t, err)
		_, err = base.GetAlertByID(ctx, alert.ID())
		require.NoError(t, err)
		_, err = base.GetQuota(ctx, model.QuotaScopeOrg, string(fixture.org))
		require.ErrorIs(t, err, port.ErrNotFound)
	})
}

// TestWebUIFrozen refuses the local writes to a frozen scope with 403 and
// its own explanation.
func TestWebUIFrozen(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		tenant, err := base.GetTenantByID(ctx, testTenantID)
		require.NoError(t, err)
		operator := model.NewUser(testTenantID, "oidc", "operator", "operator@example.test", "Operator", true, model.PlatformRoleUser, model.PlatformRoleAdmin)
		require.NoError(t, base.SaveUser(ctx, operator))
		store := lifecycleStore(t, db)
		_, err = store.FreezeResource(ctx, orgScope(testTenantID), string(fixture.org), model.MatchCondition{})
		require.NoError(t, err)

		handler := admin.NewHandler(store, store, store, store, nil, nil, nil)
		r := httptest.NewRequest(http.MethodPost, "/orgs/"+string(fixture.org)+"/edit", strings.NewReader("name=Changed&active=on"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rctx := httpCtx.SetTenant(r.Context(), tenant)
		rctx = httpCtx.SetUser(rctx, operator)
		rctx = httpCtx.SetBaseURL(rctx, "https://gateway.test")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r.WithContext(rctx))
		require.Equal(t, http.StatusForbidden, w.Code)
		require.Contains(t, w.Body.String(), "en cours de suppression")
	})
}
