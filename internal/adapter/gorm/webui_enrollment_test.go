package gorm_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui"
	"github.com/xolo-gateway/xolo/internal/http/middleware/memberships"
)

func TestWebUIEnrollmentRules(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		f := newEnrollmentFixture(t, store)
		other := newEnrollmentFixture(t, store)
		foreign := other.rule(t, model.EnrollmentKindClaim, "iss", enrollmentIssuer, model.RoleMember)

		roles, err := store.ListOrgRoles(ctx, f.org.ID())
		require.NoError(t, err)
		roleOf := func(kind string) model.Role {
			for _, role := range roles {
				if role.BuiltinKind() == kind {
					return role
				}
			}
			t.Fatalf("no %s role", kind)
			return nil
		}
		user := func(kind string) model.User {
			account := f.user(t, true)
			membership := model.NewMembership(account.ID(), f.org.ID())
			require.NoError(t, store.AddMember(ctx, membership))
			require.NoError(t, store.SetMembershipRoles(ctx, membership.ID(), []model.RoleID{roleOf(kind).ID()}))
			return account
		}
		admin, member := user(model.BuiltinKindAdmin), user(model.BuiltinKindMember)

		handler := memberships.Middleware(store, store)(webui.NewHandler(
			nil, store, store, store, store, store, store, store, store, store, service.NewInvitationService(store), store, store, nil, nil, store, scopeTestSecretKey,
			nil, nil, nil, store, store, store, store, 100, 100,
		).WithEnrollment(f.service))

		do := func(account model.User, method, path string, form url.Values) *httptest.ResponseRecorder {
			r := httptest.NewRequest(method, "/orgs/"+f.org.Slug()+"/admin/enrollment-rules"+path, strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rctx := httpCtx.SetTenant(r.Context(), f.tenant)
			rctx = httpCtx.SetUser(rctx, account)
			rctx = httpCtx.SetBaseURL(rctx, "https://gateway.test")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r.WithContext(rctx))
			return w
		}
		form := func(kind, claim, value string, role model.Role) url.Values {
			return url.Values{"name": {"Staff"}, "kind": {kind}, "claim": {claim}, "value": {value}, "role": {string(role.ID())}, "enabled": {"on"}}
		}

		w := do(admin, http.MethodPost, "", form("claim", "groups", "staff", roleOf(model.BuiltinKindMember)))
		require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
		rules, err := f.service.ListRules(ctx, f.org.ID())
		require.NoError(t, err)
		require.Len(t, rules, 1)
		rule := rules[0]
		ruleID := string(rule.ID())

		t.Run("the list and the form show the rule", func(t *testing.T) {
			w := do(admin, http.MethodGet, "", nil)
			require.Equal(t, http.StatusOK, w.Code)
			require.Contains(t, w.Body.String(), "groups")
			require.Contains(t, do(admin, http.MethodGet, "/"+ruleID+"/edit", nil).Body.String(), `value="staff"`)
		})

		t.Run("the owner role is refused and the form stays filled", func(t *testing.T) {
			w := do(admin, http.MethodPost, "", form("claim", "groups", "boss", roleOf(model.BuiltinKindOwner)))
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Contains(t, w.Body.String(), `value="boss"`)
		})

		t.Run("a domain rule drops a stray claim", func(t *testing.T) {
			w := do(admin, http.MethodPost, "/"+ruleID+"/edit", form("email_domain", "groups", "Example.org", roleOf(model.BuiltinKindMember)))
			require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
			saved, err := f.service.GetRule(ctx, f.org.ID(), rule.ID())
			require.NoError(t, err)
			require.Empty(t, saved.Claim())
			require.Equal(t, "example.org", saved.Value())
		})

		t.Run("a rule of another organization is not found", func(t *testing.T) {
			require.Equal(t, http.StatusNotFound, do(admin, http.MethodGet, "/"+string(foreign.ID())+"/edit", nil).Code)
			require.Equal(t, http.StatusNotFound, do(admin, http.MethodPost, "/"+string(foreign.ID())+"/edit", form("claim", "groups", "x", roleOf(model.BuiltinKindMember))).Code)
			require.Equal(t, http.StatusNotFound, do(admin, http.MethodPost, "/"+string(foreign.ID())+"/delete", nil).Code)
		})

		t.Run("the roles page says why a role granted by a rule stays", func(t *testing.T) {
			reader := model.NewRole(f.org.ID(), "Reader", "")
			require.NoError(t, store.CreateRole(ctx, reader))
			granting := f.rule(t, model.EnrollmentKindClaim, "groups", "readers", string(reader.ID()))
			t.Cleanup(func() {
				require.NoError(t, f.service.DeleteRule(ctx, f.tenant.ID(), f.org.ID(), granting.ID()))
			})

			roles := func(method, query string) *httptest.ResponseRecorder {
				path := "/orgs/" + f.org.Slug() + "/admin/roles"
				if method == http.MethodDelete {
					path += "/" + string(reader.ID())
				}
				r := httptest.NewRequest(method, path+query, nil)
				r.Header.Set("HX-Request", "true")
				rctx := httpCtx.SetTenant(r.Context(), f.tenant)
				rctx = httpCtx.SetUser(rctx, admin)
				rctx = httpCtx.SetBaseURL(rctx, "https://gateway.test")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r.WithContext(rctx))
				return w
			}

			w := roles(http.MethodDelete, "")
			require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
			require.Equal(t, "/orgs/"+f.org.Slug()+"/admin/roles?error=granted_by_enrollment", w.Header().Get("Location"))
			_, err := store.GetRoleByID(ctx, reader.ID())
			require.NoError(t, err)

			require.Contains(t, roles(http.MethodGet, "?error=granted_by_enrollment").Body.String(), "règle d&#39;inscription")
			require.NotContains(t, roles(http.MethodGet, "?error=Call+0800").Body.String(), "Call 0800")
		})

		t.Run("a plain member can not read the rules", func(t *testing.T) {
			require.Equal(t, http.StatusForbidden, do(member, http.MethodGet, "", nil).Code)
			require.Equal(t, http.StatusForbidden, do(member, http.MethodPost, "/"+ruleID+"/delete", nil).Code)
		})

		w = do(admin, http.MethodPost, "/"+ruleID+"/delete", nil)
		require.Equal(t, http.StatusSeeOther, w.Code)
		rules, err = f.service.ListRules(ctx, f.org.ID())
		require.NoError(t, err)
		require.Empty(t, rules)
	})
}
