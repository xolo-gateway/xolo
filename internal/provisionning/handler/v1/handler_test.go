package v1_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
	v1 "github.com/xolo-gateway/xolo/internal/provisionning/handler/v1"
	gormpkg "gorm.io/gorm"
)

func newTestHandler(t *testing.T) (handler *v1.Handler, db *gormpkg.DB, tenantBase string) {
	t.Helper()

	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	store := xologorm.NewStore(db)

	// The schema migration creates the default tenant. These tests exercise the
	// resources nested under a tenant, so every path is built from it.
	tenant, err := store.GetTenantBySlug(context.Background(), model.DefaultTenantSlug)
	if err != nil {
		t.Fatalf("get default tenant: %v", err)
	}

	handler = v1.NewHandler(service.NewProvisioningService(store, store, store, store))

	return handler, db, "/v1/tenants/" + string(tenant.ID())
}

// call performs a request against the handler. body may be nil, a string or any
// JSON-serializable value.
func call(t *testing.T, handler http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader

	switch payload := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case string:
		reader = bytes.NewReader([]byte(payload))
	default:
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}

	return payload
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()

	if rec.Code != want {
		t.Fatalf("status: got %d, want %d (body: %s)", rec.Code, want, rec.Body.String())
	}
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()

	payload := decodeBody(t, rec)

	errorBody, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("body should carry an error envelope, got %s", rec.Body.String())
	}
	if got := errorBody["code"]; got != want {
		t.Errorf("error code: got %v, want %q", got, want)
	}
	if message, _ := errorBody["message"].(string); message == "" {
		t.Error("error message should not be empty")
	}
}

// createOrganization provisions an organization through the API and returns its
// identifier.
func createOrganization(t *testing.T, handler http.Handler, tenantBase, slug string, withOwner bool) (orgID, membershipID string) {
	t.Helper()

	orgBase := tenantBase + "/organizations"

	payload := map[string]any{"slug": slug, "name": strings.ToUpper(slug)}
	if withOwner {
		payload["owner"] = map[string]any{
			"provider":    "openid-connect",
			"subject":     "sub-" + slug,
			"email":       slug + "@example.tld",
			"displayName": slug + " owner",
		}
	}

	rec := call(t, handler, http.MethodPost, orgBase, payload)
	assertStatus(t, rec, http.StatusCreated)

	body := decodeBody(t, rec)

	org, ok := body["organization"].(map[string]any)
	if !ok {
		t.Fatalf("missing organization in response: %s", rec.Body.String())
	}
	orgID, _ = org["id"].(string)

	if membership, ok := body["ownerMembership"].(map[string]any); ok {
		membershipID, _ = membership["id"].(string)
	}

	return orgID, membershipID
}

func TestHealthzAndPermissions(t *testing.T) {
	handler, _, _ := newTestHandler(t)

	rec := call(t, handler, http.MethodGet, "/v1/healthz", nil)
	assertStatus(t, rec, http.StatusOK)

	rec = call(t, handler, http.MethodGet, "/v1/permissions", nil)
	assertStatus(t, rec, http.StatusOK)

	groups, ok := decodeBody(t, rec)["groups"].([]any)
	if !ok {
		t.Fatalf("missing groups: %s", rec.Body.String())
	}
	if len(groups) != len(rbac.Catalog()) {
		t.Errorf("groups: got %d, want %d", len(groups), len(rbac.Catalog()))
	}
}

func TestOrganizationEndpoints(t *testing.T) {
	t.Run("creates an organization with its initial owner", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		rec := call(t, handler, http.MethodPost, orgBase, map[string]any{
			"slug": "acme",
			"name": "Acme",
			"owner": map[string]any{
				"provider":    "openid-connect",
				"subject":     "sub-owner",
				"email":       "owner@acme.tld",
				"displayName": "Owner",
			},
		})
		assertStatus(t, rec, http.StatusCreated)

		body := decodeBody(t, rec)

		if created, _ := body["ownerCreated"].(bool); !created {
			t.Error("ownerCreated should be true")
		}

		owner, ok := body["owner"].(map[string]any)
		if !ok {
			t.Fatalf("missing owner: %s", rec.Body.String())
		}

		roles, _ := owner["platformRoles"].([]any)
		if len(roles) != 1 || roles[0] != "user" {
			t.Errorf("platform roles: got %v, want [user]", roles)
		}

		membership, ok := body["ownerMembership"].(map[string]any)
		if !ok {
			t.Fatalf("missing ownerMembership: %s", rec.Body.String())
		}
		memberRoles, _ := membership["roles"].([]any)
		if len(memberRoles) != 1 {
			t.Fatalf("membership roles: got %v", memberRoles)
		}
		if kind := memberRoles[0].(map[string]any)["builtinKind"]; kind != "owner" {
			t.Errorf("builtin kind: got %v, want owner", kind)
		}
	})

	t.Run("reports a duplicate slug as a conflict", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		tenantID, _ := createOrganization(t, handler, tenantBase, "acme", false)

		rec := call(t, handler, http.MethodPost, orgBase, map[string]any{"slug": "acme", "name": "Acme"})
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")

		if message := decodeBody(t, rec)["error"].(map[string]any)["message"].(string); !strings.Contains(message, tenantID) {
			t.Errorf("conflict message should carry the existing id %q, got %q", tenantID, message)
		}
	})

	t.Run("rejects invalid payloads", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		cases := map[string]struct {
			body   any
			status int
			code   string
		}{
			"malformed json": {"{", http.StatusBadRequest, "invalid_request"},
			"unknown field":  {map[string]any{"slug": "acme", "name": "Acme", "nope": true}, http.StatusBadRequest, "invalid_request"},
			"invalid slug":   {map[string]any{"slug": "Acme Corp", "name": "Acme"}, http.StatusUnprocessableEntity, "unprocessable"},
			"missing name":   {map[string]any{"slug": "acme"}, http.StatusUnprocessableEntity, "unprocessable"},
			"bad currency":   {map[string]any{"slug": "acme", "name": "Acme", "currency": "XXX"}, http.StatusUnprocessableEntity, "unprocessable"},
		}

		for name, testCase := range cases {
			t.Run(name, func(t *testing.T) {
				rec := call(t, handler, http.MethodPost, orgBase, testCase.body)
				assertStatus(t, rec, testCase.status)
				assertErrorCode(t, rec, testCase.code)
			})
		}
	})

	t.Run("reads, updates and deletes an organization", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		tenantID, _ := createOrganization(t, handler, tenantBase, "acme", false)

		rec := call(t, handler, http.MethodGet, orgBase+"/"+tenantID, nil)
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, handler, http.MethodPatch, orgBase+"/"+tenantID, map[string]any{"name": "Acme Corporation"})
		assertStatus(t, rec, http.StatusOK)
		if name := decodeBody(t, rec)["name"]; name != "Acme Corporation" {
			t.Errorf("name: got %v", name)
		}

		rec = call(t, handler, http.MethodDelete, orgBase+"/"+tenantID, nil)
		assertStatus(t, rec, http.StatusNoContent)

		rec = call(t, handler, http.MethodGet, orgBase+"/"+tenantID, nil)
		assertStatus(t, rec, http.StatusNotFound)
		assertErrorCode(t, rec, "not_found")
	})

	t.Run("looks a tenant up by slug", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		createOrganization(t, handler, tenantBase, "acme", false)

		rec := call(t, handler, http.MethodGet, orgBase+"?slug=acme", nil)
		assertStatus(t, rec, http.StatusOK)
		if total := decodeBody(t, rec)["total"]; total != float64(1) {
			t.Errorf("total: got %v, want 1", total)
		}

		rec = call(t, handler, http.MethodGet, orgBase+"?slug=unknown", nil)
		assertStatus(t, rec, http.StatusOK)
		if total := decodeBody(t, rec)["total"]; total != float64(0) {
			t.Errorf("total: got %v, want 0", total)
		}
	})

	t.Run("rejects invalid pagination", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		rec := call(t, handler, http.MethodGet, orgBase+"?limit=10000", nil)
		assertStatus(t, rec, http.StatusBadRequest)
		assertErrorCode(t, rec, "invalid_request")
	})
}

func TestMemberEndpoints(t *testing.T) {
	newMember := func(subject string) map[string]any {
		return map[string]any{
			"user": map[string]any{
				"provider": "openid-connect",
				"subject":  subject,
				"email":    subject + "@acme.tld",
			},
			"builtinRoles": []string{"member"},
		}
	}

	t.Run("adds, reads, updates and removes a member", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		tenantID, _ := createOrganization(t, handler, tenantBase, "acme", true)

		rec := call(t, handler, http.MethodPost, orgBase+"/"+tenantID+"/members", newMember("sub-member"))
		assertStatus(t, rec, http.StatusCreated)

		membershipID := decodeBody(t, rec)["id"].(string)

		rec = call(t, handler, http.MethodGet, orgBase+"/"+tenantID+"/members/"+membershipID, nil)
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, handler, http.MethodPut, orgBase+"/"+tenantID+"/members/"+membershipID+"/roles",
			map[string]any{"builtinRoles": []string{"admin"}})
		assertStatus(t, rec, http.StatusOK)

		roles := decodeBody(t, rec)["roles"].([]any)
		if len(roles) != 1 || roles[0].(map[string]any)["builtinKind"] != "admin" {
			t.Errorf("roles: got %v", roles)
		}

		rec = call(t, handler, http.MethodGet, orgBase+"/"+tenantID+"/members", nil)
		assertStatus(t, rec, http.StatusOK)
		if total := decodeBody(t, rec)["total"]; total != float64(2) {
			t.Errorf("total: got %v, want 2", total)
		}

		rec = call(t, handler, http.MethodDelete, orgBase+"/"+tenantID+"/members/"+membershipID, nil)
		assertStatus(t, rec, http.StatusNoContent)
	})

	t.Run("reports a duplicate membership as a conflict", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		tenantID, _ := createOrganization(t, handler, tenantBase, "acme", true)

		rec := call(t, handler, http.MethodPost, orgBase+"/"+tenantID+"/members", newMember("sub-member"))
		assertStatus(t, rec, http.StatusCreated)

		rec = call(t, handler, http.MethodPost, orgBase+"/"+tenantID+"/members", newMember("sub-member"))
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")
	})

	t.Run("refuses a role belonging to another tenant", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		acmeID, _ := createOrganization(t, handler, tenantBase, "acme", true)
		otherID, _ := createOrganization(t, handler, tenantBase, "other", false)

		rec := call(t, handler, http.MethodGet, orgBase+"/"+otherID+"/roles", nil)
		assertStatus(t, rec, http.StatusOK)
		otherRoleID := decodeBody(t, rec)["items"].([]any)[0].(map[string]any)["id"].(string)

		rec = call(t, handler, http.MethodPost, orgBase+"/"+acmeID+"/members", newMember("sub-member"))
		assertStatus(t, rec, http.StatusCreated)
		membershipID := decodeBody(t, rec)["id"].(string)

		rec = call(t, handler, http.MethodPut, orgBase+"/"+acmeID+"/members/"+membershipID+"/roles",
			map[string]any{"roleIds": []string{otherRoleID}})
		assertStatus(t, rec, http.StatusUnprocessableEntity)
		assertErrorCode(t, rec, "unprocessable")
	})

	t.Run("refuses to drop the last owner", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		tenantID, ownerMembershipID := createOrganization(t, handler, tenantBase, "acme", true)

		rec := call(t, handler, http.MethodDelete, orgBase+"/"+tenantID+"/members/"+ownerMembershipID, nil)
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")

		rec = call(t, handler, http.MethodPut, orgBase+"/"+tenantID+"/members/"+ownerMembershipID+"/roles",
			map[string]any{"builtinRoles": []string{"member"}})
		assertStatus(t, rec, http.StatusConflict)
	})

	t.Run("hides resources of another tenant", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		acmeID, acmeMembershipID := createOrganization(t, handler, tenantBase, "acme", true)
		otherID, _ := createOrganization(t, handler, tenantBase, "other", false)

		rec := call(t, handler, http.MethodGet, orgBase+"/"+otherID+"/members/"+acmeMembershipID, nil)
		assertStatus(t, rec, http.StatusNotFound)
		assertErrorCode(t, rec, "not_found")

		rec = call(t, handler, http.MethodGet, orgBase+"/"+acmeID+"/members/nope", nil)
		assertStatus(t, rec, http.StatusNotFound)

		rec = call(t, handler, http.MethodGet, orgBase+"/nope/members", nil)
		assertStatus(t, rec, http.StatusNotFound)
	})
}

func TestRoleEndpoints(t *testing.T) {
	t.Run("creates, updates and deletes a custom role", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		tenantID, _ := createOrganization(t, handler, tenantBase, "acme", false)

		rec := call(t, handler, http.MethodPost, orgBase+"/"+tenantID+"/roles", map[string]any{
			"name":        "auditor",
			"permissions": []string{"usage:read"},
		})
		assertStatus(t, rec, http.StatusCreated)

		roleID := decodeBody(t, rec)["id"].(string)

		rec = call(t, handler, http.MethodPut, orgBase+"/"+tenantID+"/roles/"+roleID, map[string]any{
			"permissions": []string{"usage:read", "members:read"},
		})
		assertStatus(t, rec, http.StatusOK)
		if permissions := decodeBody(t, rec)["permissions"].([]any); len(permissions) != 2 {
			t.Errorf("permissions: got %v", permissions)
		}

		rec = call(t, handler, http.MethodDelete, orgBase+"/"+tenantID+"/roles/"+roleID, nil)
		assertStatus(t, rec, http.StatusNoContent)
	})

	t.Run("refuses an unknown permission", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		tenantID, _ := createOrganization(t, handler, tenantBase, "acme", false)

		rec := call(t, handler, http.MethodPost, orgBase+"/"+tenantID+"/roles", map[string]any{
			"name":        "bogus",
			"permissions": []string{"not:a:permission"},
		})
		assertStatus(t, rec, http.StatusUnprocessableEntity)
		assertErrorCode(t, rec, "unprocessable")
	})

	t.Run("refuses a duplicate role name", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		tenantID, _ := createOrganization(t, handler, tenantBase, "acme", false)

		rec := call(t, handler, http.MethodPost, orgBase+"/"+tenantID+"/roles", map[string]any{"name": "auditor"})
		assertStatus(t, rec, http.StatusCreated)

		rec = call(t, handler, http.MethodPost, orgBase+"/"+tenantID+"/roles", map[string]any{"name": "auditor"})
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")
	})

	t.Run("protects builtin roles", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)
		orgBase := tenantBase + "/organizations"

		tenantID, _ := createOrganization(t, handler, tenantBase, "acme", false)

		rec := call(t, handler, http.MethodGet, orgBase+"/"+tenantID+"/roles", nil)
		assertStatus(t, rec, http.StatusOK)

		var builtinID string
		for _, raw := range decodeBody(t, rec)["items"].([]any) {
			role := raw.(map[string]any)
			if role["builtinKind"] == "owner" {
				builtinID = role["id"].(string)
			}
		}
		if builtinID == "" {
			t.Fatal("no builtin owner role found")
		}

		rec = call(t, handler, http.MethodPut, orgBase+"/"+tenantID+"/roles/"+builtinID, map[string]any{"name": "hacked"})
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")

		rec = call(t, handler, http.MethodDelete, orgBase+"/"+tenantID+"/roles/"+builtinID, nil)
		assertStatus(t, rec, http.StatusConflict)
	})
}

func TestUserEndpoints(t *testing.T) {
	identity := map[string]any{
		"provider":    "openid-connect",
		"subject":     "sub-1",
		"email":       "user@acme.tld",
		"displayName": "User",
	}

	t.Run("upserts a user idempotently", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)

		rec := call(t, handler, http.MethodPut, tenantBase+"/users", identity)
		assertStatus(t, rec, http.StatusCreated)
		userID := decodeBody(t, rec)["id"].(string)

		rec = call(t, handler, http.MethodPut, tenantBase+"/users", identity)
		assertStatus(t, rec, http.StatusOK)
		if id := decodeBody(t, rec)["id"]; id != userID {
			t.Errorf("id: got %v, want %q", id, userID)
		}
	})

	t.Run("looks a user up by identity", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)

		call(t, handler, http.MethodPut, tenantBase+"/users", identity)

		rec := call(t, handler, http.MethodGet, tenantBase+"/users?provider=openid-connect&subject=sub-1", nil)
		assertStatus(t, rec, http.StatusOK)
		if total := decodeBody(t, rec)["total"]; total != float64(1) {
			t.Errorf("total: got %v, want 1", total)
		}

		rec = call(t, handler, http.MethodGet, tenantBase+"/users?provider=openid-connect&subject=unknown", nil)
		assertStatus(t, rec, http.StatusOK)
		if total := decodeBody(t, rec)["total"]; total != float64(0) {
			t.Errorf("total: got %v, want 0", total)
		}

		rec = call(t, handler, http.MethodGet, tenantBase+"/users?provider=openid-connect", nil)
		assertStatus(t, rec, http.StatusBadRequest)
		assertErrorCode(t, rec, "invalid_request")
	})

	t.Run("updates and reads a user", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)

		rec := call(t, handler, http.MethodPut, tenantBase+"/users", identity)
		userID := decodeBody(t, rec)["id"].(string)

		rec = call(t, handler, http.MethodPatch, tenantBase+"/users/"+userID, map[string]any{"displayName": "Renamed"})
		assertStatus(t, rec, http.StatusOK)
		if name := decodeBody(t, rec)["displayName"]; name != "Renamed" {
			t.Errorf("display name: got %v", name)
		}

		rec = call(t, handler, http.MethodGet, tenantBase+"/users/"+userID, nil)
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, handler, http.MethodGet, tenantBase+"/users/nope", nil)
		assertStatus(t, rec, http.StatusNotFound)
	})

	t.Run("filters on the active flag", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)

		active := false
		call(t, handler, http.MethodPut, tenantBase+"/users", identity)
		call(t, handler, http.MethodPut, tenantBase+"/users", map[string]any{
			"provider": "openid-connect",
			"subject":  "sub-pending",
			"email":    "pending@acme.tld",
			"active":   active,
		})

		rec := call(t, handler, http.MethodGet, tenantBase+"/users?active=false", nil)
		assertStatus(t, rec, http.StatusOK)

		items := decodeBody(t, rec)["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("inactive users: got %d, want 1", len(items))
		}
		if email := items[0].(map[string]any)["email"]; email != "pending@acme.tld" {
			t.Errorf("email: got %v", email)
		}

		rec = call(t, handler, http.MethodGet, tenantBase+"/users?active=true", nil)
		assertStatus(t, rec, http.StatusOK)
		if total := decodeBody(t, rec)["total"]; total != float64(1) {
			t.Errorf("active users: got %v, want 1", total)
		}

		rec = call(t, handler, http.MethodGet, tenantBase+"/users?active=maybe", nil)
		assertStatus(t, rec, http.StatusBadRequest)
		assertErrorCode(t, rec, "invalid_request")
	})

	t.Run("never exposes platform role management", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)

		rec := call(t, handler, http.MethodPut, tenantBase+"/users", identity)
		userID := decodeBody(t, rec)["id"].(string)

		rec = call(t, handler, http.MethodPatch, tenantBase+"/users/"+userID, map[string]any{"platformRoles": []string{"admin"}})
		assertStatus(t, rec, http.StatusBadRequest)
		assertErrorCode(t, rec, "invalid_request")

		rec = call(t, handler, http.MethodGet, tenantBase+"/users/"+userID, nil)
		roles := decodeBody(t, rec)["platformRoles"].([]any)
		if len(roles) != 1 || roles[0] != "user" {
			t.Errorf("platform roles: got %v, want [user]", roles)
		}
	})
}

func TestRoutingFallbacks(t *testing.T) {
	handler, _, _ := newTestHandler(t)

	rec := call(t, handler, http.MethodGet, "/v1/unknown", nil)
	assertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, "not_found")

	rec = call(t, handler, http.MethodDelete, "/v1/permissions", nil)
	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

// TestInternalErrorsDoNotLeak checks that an unexpected failure is reported as
// a generic error: no SQL, no file path, no stack trace.
func TestInternalErrorsDoNotLeak(t *testing.T) {
	handler, db, tenantBase := newTestHandler(t)
	orgBase := tenantBase + "/organizations"

	createOrganization(t, handler, tenantBase, "acme", false)

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	rec := call(t, handler, http.MethodGet, orgBase, nil)
	assertStatus(t, rec, http.StatusInternalServerError)
	assertErrorCode(t, rec, "internal_error")

	body := rec.Body.String()
	for _, leak := range []string{"sql", "SELECT", "gorm", ".go:", "/home/", "database"} {
		if strings.Contains(body, leak) {
			t.Errorf("error body should not contain %q, got %s", leak, body)
		}
	}
}

// newMultiTenantHandler builds a handler allowed to provision tenants, as
// XOLO_MULTITENANCY_ENABLED=true does.
func newMultiTenantHandler(t *testing.T) (*v1.Handler, string) {
	t.Helper()

	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	store := xologorm.NewStore(db)

	tenant, err := store.GetTenantBySlug(context.Background(), model.DefaultTenantSlug)
	if err != nil {
		t.Fatalf("get default tenant: %v", err)
	}

	handler := v1.NewHandler(service.NewProvisioningService(store, store, store, store,
		service.WithMultiTenant(true),
	))

	return handler, string(tenant.ID())
}

func TestTenantEndpoints(t *testing.T) {
	t.Run("single-tenant mode refuses to create a tenant", func(t *testing.T) {
		handler, _, _ := newTestHandler(t)

		rec := call(t, handler, http.MethodPost, "/v1/tenants", map[string]any{"slug": "acme", "name": "Acme"})
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")

		if message := decodeBody(t, rec)["error"].(map[string]any)["message"].(string); !strings.Contains(message, "single-tenant") {
			t.Errorf("message should explain the mode, got %q", message)
		}
	})

	t.Run("single-tenant mode still exposes its tenant", func(t *testing.T) {
		handler, _, tenantBase := newTestHandler(t)

		// A control plane discovers the identifier every nested route needs
		// through the slug lookup, without any tenant having been created.
		rec := call(t, handler, http.MethodGet, "/v1/tenants?slug="+model.DefaultTenantSlug, nil)
		assertStatus(t, rec, http.StatusOK)

		items, _ := decodeBody(t, rec)["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("items: got %d, want 1", len(items))
		}
		if id, _ := items[0].(map[string]any)["id"].(string); !strings.HasSuffix(tenantBase, id) {
			t.Errorf("id %q should be the one every path is built from (%q)", id, tenantBase)
		}
	})

	t.Run("creates, reads, updates and deletes a tenant", func(t *testing.T) {
		handler, _ := newMultiTenantHandler(t)

		rec := call(t, handler, http.MethodPost, "/v1/tenants", map[string]any{
			"slug": "acme", "name": "Acme", "description": "Acme Inc.",
		})
		assertStatus(t, rec, http.StatusCreated)

		tenantID, _ := decodeBody(t, rec)["id"].(string)
		if tenantID == "" {
			t.Fatalf("missing tenant id: %s", rec.Body.String())
		}

		rec = call(t, handler, http.MethodGet, "/v1/tenants/"+tenantID, nil)
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, handler, http.MethodPatch, "/v1/tenants/"+tenantID, map[string]any{"name": "Acme Corporation"})
		assertStatus(t, rec, http.StatusOK)
		if name := decodeBody(t, rec)["name"]; name != "Acme Corporation" {
			t.Errorf("name: got %v, want %q", name, "Acme Corporation")
		}

		rec = call(t, handler, http.MethodDelete, "/v1/tenants/"+tenantID, nil)
		assertStatus(t, rec, http.StatusNoContent)

		rec = call(t, handler, http.MethodGet, "/v1/tenants/"+tenantID, nil)
		assertStatus(t, rec, http.StatusNotFound)
	})

	t.Run("reports a duplicate slug as a conflict carrying the existing id", func(t *testing.T) {
		handler, defaultTenantID := newMultiTenantHandler(t)

		rec := call(t, handler, http.MethodPost, "/v1/tenants",
			map[string]any{"slug": model.DefaultTenantSlug, "name": "Another default"})
		assertStatus(t, rec, http.StatusConflict)

		if message := decodeBody(t, rec)["error"].(map[string]any)["message"].(string); !strings.Contains(message, defaultTenantID) {
			t.Errorf("conflict message should carry %q, got %q", defaultTenantID, message)
		}
	})

	t.Run("protects the default tenant", func(t *testing.T) {
		handler, defaultTenantID := newMultiTenantHandler(t)

		rec := call(t, handler, http.MethodDelete, "/v1/tenants/"+defaultTenantID, nil)
		assertStatus(t, rec, http.StatusConflict)

		rec = call(t, handler, http.MethodPatch, "/v1/tenants/"+defaultTenantID, map[string]any{"active": false})
		assertStatus(t, rec, http.StatusConflict)
	})

	t.Run("rejects invalid payloads", func(t *testing.T) {
		handler, _ := newMultiTenantHandler(t)

		cases := map[string]struct {
			body   any
			status int
			code   string
		}{
			"malformed json": {"{", http.StatusBadRequest, "invalid_request"},
			"unknown field":  {map[string]any{"slug": "acme", "name": "Acme", "currency": "EUR"}, http.StatusBadRequest, "invalid_request"},
			"invalid slug":   {map[string]any{"slug": "Acme Corp", "name": "Acme"}, http.StatusUnprocessableEntity, "unprocessable"},
			"missing name":   {map[string]any{"slug": "acme"}, http.StatusUnprocessableEntity, "unprocessable"},
		}

		for name, testCase := range cases {
			t.Run(name, func(t *testing.T) {
				rec := call(t, handler, http.MethodPost, "/v1/tenants", testCase.body)
				assertStatus(t, rec, testCase.status)
				assertErrorCode(t, rec, testCase.code)
			})
		}
	})

	t.Run("hides the resources of another tenant", func(t *testing.T) {
		handler, _ := newMultiTenantHandler(t)

		rec := call(t, handler, http.MethodPost, "/v1/tenants", map[string]any{"slug": "acme", "name": "Acme"})
		assertStatus(t, rec, http.StatusCreated)
		otherTenantID, _ := decodeBody(t, rec)["id"].(string)

		// An organization created in the default tenant must not be reachable
		// through another tenant's path, even with the right identifier.
		defaultBase := "/v1/tenants/" + func() string {
			rec := call(t, handler, http.MethodGet, "/v1/tenants?slug="+model.DefaultTenantSlug, nil)
			items, _ := decodeBody(t, rec)["items"].([]any)
			id, _ := items[0].(map[string]any)["id"].(string)
			return id
		}()

		orgID, _ := createOrganization(t, handler, defaultBase, "acme", false)

		otherBase := "/v1/tenants/" + otherTenantID + "/organizations"

		rec = call(t, handler, http.MethodGet, otherBase+"/"+orgID, nil)
		assertStatus(t, rec, http.StatusNotFound)

		rec = call(t, handler, http.MethodGet, otherBase+"/"+orgID+"/members", nil)
		assertStatus(t, rec, http.StatusNotFound)

		// The same slug is free on the other tenant: isolation, not collision.
		rec = call(t, handler, http.MethodPost, otherBase, map[string]any{"slug": "acme", "name": "Acme"})
		assertStatus(t, rec, http.StatusCreated)
	})

	t.Run("an unknown tenant is reported as not found", func(t *testing.T) {
		handler, _, _ := newTestHandler(t)

		for _, target := range []string{
			"/v1/tenants/nope",
			"/v1/tenants/nope/organizations",
			"/v1/tenants/nope/users",
		} {
			rec := call(t, handler, http.MethodGet, target, nil)
			assertStatus(t, rec, http.StatusNotFound)
			assertErrorCode(t, rec, "not_found")
		}
	})
}
