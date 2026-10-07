package v1_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
	v1 "github.com/xolo-gateway/xolo/internal/provisionning/handler/v1"
	gormpkg "gorm.io/gorm"
)

const testVersion = "1.2.3-test"

// testEnv is a handler over an in-memory database whose schema migration
// created the default tenant. Every path of the tests is built from it.
type testEnv struct {
	handler *v1.Handler
	store   *xologorm.Store
	db      *gormpkg.DB

	// tenantID is the default tenant. tenantBase addresses it on the common
	// contract, xoloBase on the Xolo extensions.
	tenantID   string
	tenantBase string
	xoloBase   string
}

func newEnv(t *testing.T, options ...service.ProvisioningServiceOptionFunc) *testEnv {
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

	options = append([]service.ProvisioningServiceOptionFunc{service.WithProvisioningTransaction(store), service.WithProvisioningReader(store)}, options...)
	handler := v1.NewHandler(service.NewProvisioningService(store, store, store, store, options...), testVersion)

	tenantID := string(tenant.ID())

	return &testEnv{
		handler:    handler,
		store:      store,
		db:         db,
		tenantID:   tenantID,
		tenantBase: "/v1/tenants/" + tenantID,
		xoloBase:   "/v1/xolo/tenants/" + tenantID,
	}
}

// newMultiTenantEnv builds a handler allowed to provision tenants, as
// XOLO_MULTITENANCY_ENABLED=true does.
func newMultiTenantEnv(t *testing.T) *testEnv {
	t.Helper()
	return newEnv(t, service.WithMultiTenant(true))
}

// call performs a JSON request against the handler. body may be nil, a string
// or any JSON-serializable value.
func call(t *testing.T, handler http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return callWithContentType(t, handler, method, target, "application/json", body)
}

// callWithContentType performs a request with an explicit Content-Type; an
// empty contentType sends none.
func callWithContentType(t *testing.T, handler http.Handler, method, target, contentType string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader

	switch payload := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case string:
		reader = bytes.NewReader([]byte(payload))
	case []byte:
		reader = bytes.NewReader(payload)
	default:
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, target, reader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

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

func resource(slug string) map[string]any {
	return map[string]any{"slug": slug, "name": strings.ToUpper(slug), "status": "active"}
}

func memberBody(email string) map[string]any {
	return map[string]any{"email": email, "tenant_role": "member", "status": "active"}
}

func membershipBody(role string) map[string]any {
	return map[string]any{"role": role, "status": "active"}
}

// putOrganization declares an organization through the common contract and
// returns its client-chosen identifier.
func putOrganization(t *testing.T, handler http.Handler, tenantID, slug string) string {
	t.Helper()

	orgID := uuid.NewString()

	rec := call(t, handler, http.MethodPut, "/v1/tenants/"+tenantID+"/organizations/"+orgID, resource(slug))
	assertStatus(t, rec, http.StatusOK)

	return orgID
}

// createUser registers a user of the tenant, as a sign-in would, through the
// identity upsert of the extensions and returns its identifier. The common
// member PUT only updates existing users.
func createUser(t *testing.T, handler http.Handler, tenantID, subject string) string {
	t.Helper()

	rec := call(t, handler, http.MethodPut, "/v1/xolo/tenants/"+tenantID+"/users", map[string]any{
		"provider": "openid-connect",
		"subject":  subject,
	})
	assertStatus(t, rec, http.StatusCreated)

	return decodeBody(t, rec)["id"].(string)
}

// putMember registers a user of the tenant, declares it as a member through
// the common contract and returns its user identifier.
func putMember(t *testing.T, handler http.Handler, tenantID, email string) string {
	t.Helper()

	userID := createUser(t, handler, tenantID, "sub-"+email)

	rec := call(t, handler, http.MethodPut, "/v1/tenants/"+tenantID+"/members/"+userID, memberBody(email))
	assertStatus(t, rec, http.StatusOK)

	return userID
}

// addOrgMember declares a tenant member, adds it to the organization with the
// common role and returns its user and membership identifiers. The membership
// identifier is Xolo's own: it is read back through the extensions.
func addOrgMember(t *testing.T, handler http.Handler, tenantID, orgID, email, role string) (userID, membershipID string) {
	t.Helper()

	userID = putMember(t, handler, tenantID, email)

	rec := call(t, handler, http.MethodPut, "/v1/tenants/"+tenantID+"/organizations/"+orgID+"/members/"+userID, membershipBody(role))
	assertStatus(t, rec, http.StatusOK)

	return userID, findMembershipID(t, handler, tenantID, orgID, userID)
}

func findMembershipID(t *testing.T, handler http.Handler, tenantID, orgID, userID string) string {
	t.Helper()

	rec := call(t, handler, http.MethodGet, "/v1/xolo/tenants/"+tenantID+"/organizations/"+orgID+"/members?limit=200", nil)
	assertStatus(t, rec, http.StatusOK)

	for _, raw := range decodeBody(t, rec)["items"].([]any) {
		membership := raw.(map[string]any)
		if membership["userId"] == userID {
			return membership["id"].(string)
		}
	}

	t.Fatalf("no membership of user %s in organization %s: %s", userID, orgID, rec.Body.String())
	return ""
}

// createOrganization provisions an organization through the common PUTs, with
// an initial owner if requested, and returns its identifier and the owner's
// membership identifier.
func createOrganization(t *testing.T, handler http.Handler, tenantID, slug string, withOwner bool) (orgID, membershipID string) {
	t.Helper()

	orgID = putOrganization(t, handler, tenantID, slug)

	if withOwner {
		_, membershipID = addOrgMember(t, handler, tenantID, orgID, slug+"-owner@example.tld", "owner")
	}

	return orgID, membershipID
}

// ── Common contract ─────────────────────────────────────────────────────────

func TestManifest(t *testing.T) {
	env := newEnv(t)

	rec := call(t, env.handler, http.MethodGet, "/v1/manifest", nil)
	assertStatus(t, rec, http.StatusOK)

	body := decodeBody(t, rec)
	want := map[string]any{"name": "Xolo", "version": testVersion, "contract_version": v1.ContractVersion, "capabilities": []any{"conditional_writes", "events", "reads"}}
	for key, value := range want {
		if !reflect.DeepEqual(body[key], value) {
			t.Errorf("%s: got %v, want %v", key, body[key], value)
		}
	}
	if len(body) != len(want) {
		t.Errorf("manifest should only carry %v, got %v", want, body)
	}
}

func TestCommonPutsAreIdempotent(t *testing.T) {
	env := newMultiTenantEnv(t)

	tenantID := uuid.NewString()
	orgID := uuid.NewString()

	// The member PUT never creates a user: the user exists once the tenant
	// does, as after a first sign-in.
	var userID string

	tenantPath := "/v1/tenants/" + tenantID
	steps := []struct {
		name   string
		target func() string
		body   map[string]any
	}{
		{"tenant", func() string { return tenantPath }, resource("acme")},
		{"domain", func() string { return tenantPath + "/domains/acme.example.com" }, map[string]any{"status": "active"}},
		{"organization", func() string { return tenantPath + "/organizations/" + orgID }, resource("research")},
		{"member", func() string {
			userID = createUser(t, env.handler, tenantID, "sub-jane")
			return tenantPath + "/members/" + userID
		}, map[string]any{
			"email": "jane@acme.tld", "display_name": "Jane", "tenant_role": "owner", "status": "active",
		}},
		{"membership", func() string { return tenantPath + "/organizations/" + orgID + "/members/" + userID }, membershipBody("owner")},
	}

	for _, step := range steps {
		target := step.target()
		t.Run(step.name, func(t *testing.T) {
			first := call(t, env.handler, http.MethodPut, target, step.body)
			assertStatus(t, first, http.StatusOK)

			for key, value := range step.body {
				if got := decodeBody(t, first)[key]; got != value {
					t.Errorf("%s: got %v, want %v", key, got, value)
				}
			}

			second := call(t, env.handler, http.MethodPut, target, step.body)
			assertStatus(t, second, http.StatusOK)

			if first.Body.String() != second.Body.String() {
				t.Errorf("replay should answer the same representation: %s then %s", first.Body.String(), second.Body.String())
			}
		})
	}

	t.Run("the default tenant accepts its own representation", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodGet, env.xoloBase, nil)
		assertStatus(t, rec, http.StatusOK)
		current := decodeBody(t, rec)

		body := map[string]any{"slug": current["slug"], "name": current["name"], "status": "active"}
		for range 2 {
			rec = call(t, env.handler, http.MethodPut, env.tenantBase, body)
			assertStatus(t, rec, http.StatusOK)
		}
	})

	t.Run("the writes are visible through the extensions", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodGet, "/v1/xolo/tenants/"+tenantID, nil)
		assertStatus(t, rec, http.StatusOK)
		if slug := decodeBody(t, rec)["slug"]; slug != "acme" {
			t.Errorf("tenant slug: got %v", slug)
		}

		rec = call(t, env.handler, http.MethodGet, "/v1/xolo/tenants/"+tenantID+"/organizations/"+orgID, nil)
		assertStatus(t, rec, http.StatusOK)
		if slug := decodeBody(t, rec)["slug"]; slug != "research" {
			t.Errorf("organization slug: got %v", slug)
		}

		// The organization was created with its builtin roles.
		rec = call(t, env.handler, http.MethodGet, "/v1/xolo/tenants/"+tenantID+"/organizations/"+orgID+"/roles", nil)
		assertStatus(t, rec, http.StatusOK)
		kinds := map[string]bool{}
		for _, raw := range decodeBody(t, rec)["items"].([]any) {
			if kind, _ := raw.(map[string]any)["builtinKind"].(string); kind != "" {
				kinds[kind] = true
			}
		}
		for _, kind := range []string{"owner", "admin", "member"} {
			if !kinds[kind] {
				t.Errorf("missing builtin %s role, got %v", kind, kinds)
			}
		}

		rec = call(t, env.handler, http.MethodGet, "/v1/xolo/tenants/"+tenantID+"/users/"+userID, nil)
		assertStatus(t, rec, http.StatusOK)
		user := decodeBody(t, rec)
		if user["email"] != "jane@acme.tld" || user["displayName"] != "Jane" || user["provider"] != "openid-connect" || user["subject"] != "sub-jane" {
			t.Errorf("user: got %v", user)
		}
		roles, _ := user["platformRoles"].([]any)
		if len(roles) != 1 || roles[0] != "user" {
			t.Errorf("platform roles: got %v, want [user]", roles)
		}

		membershipID := findMembershipID(t, env.handler, tenantID, orgID, userID)
		rec = call(t, env.handler, http.MethodGet, "/v1/xolo/tenants/"+tenantID+"/organizations/"+orgID+"/members/"+membershipID, nil)
		assertStatus(t, rec, http.StatusOK)
		memberRoles, _ := decodeBody(t, rec)["roles"].([]any)
		if len(memberRoles) != 1 || memberRoles[0].(map[string]any)["builtinKind"] != "owner" {
			t.Errorf("membership roles: got %v", memberRoles)
		}
	})

	t.Run("a changed representation is applied", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodPut, tenantPath+"/organizations/"+orgID,
			map[string]any{"slug": "research", "name": "Research Lab", "status": "suspended"})
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, env.handler, http.MethodGet, "/v1/xolo/tenants/"+tenantID+"/organizations/"+orgID, nil)
		assertStatus(t, rec, http.StatusOK)
		org := decodeBody(t, rec)
		if org["name"] != "Research Lab" || org["active"] != false {
			t.Errorf("organization: got %v", org)
		}
	})
}

func TestCommonRepresentations(t *testing.T) {
	env := newEnv(t)
	target := env.tenantBase + "/organizations/" + uuid.NewString()

	t.Run("requires application/json", func(t *testing.T) {
		for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded", "application/jsonx"} {
			rec := callWithContentType(t, env.handler, http.MethodPut, target, contentType, resource("acme"))
			assertStatus(t, rec, http.StatusUnsupportedMediaType)
			assertErrorCode(t, rec, "unsupported_media_type")
		}

		rec := callWithContentType(t, env.handler, http.MethodPut, target, "application/json; charset=utf-8", resource("acme"))
		assertStatus(t, rec, http.StatusOK)
	})

	cases := map[string]struct {
		body   any
		status int
		code   string
	}{
		"malformed json":      {`{`, http.StatusBadRequest, "invalid_json"},
		"empty body":          {``, http.StatusBadRequest, "invalid_json"},
		"array":               {`[]`, http.StatusBadRequest, "invalid_json"},
		"trailing data":       {`{"slug":"acme","name":"Acme","status":"active"} {}`, http.StatusBadRequest, "invalid_json"},
		"unknown field":       {`{"slug":"acme","name":"Acme","status":"active","currency":"EUR"}`, http.StatusBadRequest, "invalid_json"},
		"number":              {`{"slug":"acme","name":42,"status":"active"}`, http.StatusBadRequest, "invalid_json"},
		"boolean":             {`{"slug":"acme","name":"Acme","status":true}`, http.StatusBadRequest, "invalid_json"},
		"lone high surrogate": {`{"slug":"acme","name":"\ud800","status":"active"}`, http.StatusBadRequest, "invalid_json"},
		"lone low surrogate":  {`{"slug":"acme","name":"a\udc00b","status":"active"}`, http.StatusBadRequest, "invalid_json"},
		"invalid utf-8":       {[]byte("{\"slug\":\"acme\",\"name\":\"\xff\",\"status\":\"active\"}"), http.StatusBadRequest, "invalid_json"},
		"too large": {
			`{"slug":"acme","name":"` + strings.Repeat("a", 1<<20) + `","status":"active"}`,
			http.StatusBadRequest, "invalid_json",
		},
		"null document":  {`null`, http.StatusBadRequest, "invalid_representation"},
		"null field":     {`{"slug":"acme","name":null,"status":"active"}`, http.StatusBadRequest, "invalid_representation"},
		"missing field":  {`{"slug":"acme","name":"Acme"}`, http.StatusBadRequest, "invalid_representation"},
		"invalid slug":   {`{"slug":"Acme Corp!","name":"Acme","status":"active"}`, http.StatusUnprocessableEntity, "unprocessable"},
		"invalid status": {`{"slug":"acme","name":"Acme","status":"deleted"}`, http.StatusUnprocessableEntity, "unprocessable"},
		"empty name":     {`{"slug":"acme","name":"  ","status":"active"}`, http.StatusUnprocessableEntity, "unprocessable"},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			rec := call(t, env.handler, http.MethodPut, env.tenantBase+"/organizations/"+uuid.NewString(), testCase.body)
			assertStatus(t, rec, testCase.status)
			assertErrorCode(t, rec, testCase.code)
		})
	}

	t.Run("accepts escaped and literal non-ASCII text", func(t *testing.T) {
		for body, want := range map[string]string{
			`{"slug":"replacement","name":"�","status":"active"}`:       "�",
			`{"slug":"literal","name":"` + "�" + `","status":"active"}`: "�",
			`{"slug":"pair","name":"😀","status":"active"}`:              "\U0001F600",
		} {
			orgID := uuid.NewString()

			rec := call(t, env.handler, http.MethodPut, env.tenantBase+"/organizations/"+orgID, body)
			assertStatus(t, rec, http.StatusOK)
			if name := decodeBody(t, rec)["name"]; name != want {
				t.Errorf("name: got %q, want %q", name, want)
			}
		}
	})

	t.Run("an optional field may be omitted but not null", func(t *testing.T) {
		target := env.tenantBase + "/members/" + createUser(t, env.handler, env.tenantID, "sub-optional")

		rec := call(t, env.handler, http.MethodPut, target, `{"email":"a@acme.tld","tenant_role":"member","status":"active","display_name":null}`)
		assertStatus(t, rec, http.StatusBadRequest)
		assertErrorCode(t, rec, "invalid_representation")

		rec = call(t, env.handler, http.MethodPut, target, `{"email":"a@acme.tld","tenant_role":"member","status":"active"}`)
		assertStatus(t, rec, http.StatusOK)
	})
}

func TestCommonIdentifiers(t *testing.T) {
	env := newEnv(t)
	orgID := putOrganization(t, env.handler, env.tenantID, "acme")
	userID := putMember(t, env.handler, env.tenantID, "jane@acme.tld")

	invalid := []string{
		"nope",
		strings.ToUpper(uuid.NewString()),
		"{" + uuid.NewString() + "}",
		strings.ReplaceAll(uuid.NewString(), "-", ""),
		"urn:uuid:" + uuid.NewString(),
	}

	for _, id := range invalid {
		requests := []struct {
			target string
			body   map[string]any
		}{
			{"/v1/tenants/" + id, resource("acme")},
			{"/v1/tenants/" + id + "/domains/acme.example.com", map[string]any{"status": "active"}},
			{"/v1/tenants/" + id + "/organizations/" + orgID, resource("acme")},
			{env.tenantBase + "/organizations/" + id, resource("acme")},
			{"/v1/tenants/" + id + "/members/" + userID, memberBody("jane@acme.tld")},
			{env.tenantBase + "/members/" + id, memberBody("jane@acme.tld")},
			{"/v1/tenants/" + id + "/organizations/" + orgID + "/members/" + userID, membershipBody("member")},
			{env.tenantBase + "/organizations/" + id + "/members/" + userID, membershipBody("member")},
			{env.tenantBase + "/organizations/" + orgID + "/members/" + id, membershipBody("member")},
		}

		for _, request := range requests {
			rec := call(t, env.handler, http.MethodPut, request.target, request.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("PUT %s: got %d, want 400 (body: %s)", request.target, rec.Code, rec.Body.String())
				continue
			}
			assertErrorCode(t, rec, "invalid_parameter")
		}
	}
}

func TestCommonQueryParameters(t *testing.T) {
	env := newEnv(t)
	orgID := uuid.NewString()
	userID := uuid.NewString()

	for _, c := range []struct {
		method string
		target string
		body   any
	}{
		{http.MethodGet, "/v1/manifest?pretty=1", nil},
		{http.MethodPut, env.tenantBase + "?dry_run=true", resource("acme")},
		{http.MethodPut, env.tenantBase + "/domains/acme.example.com?dry_run=true", map[string]any{"status": "active"}},
		{http.MethodPut, env.tenantBase + "/organizations/" + orgID + "?dry_run=true", resource("acme")},
		{http.MethodPut, env.tenantBase + "/organizations/" + orgID + "?slug=acme", resource("acme")},
		{http.MethodPut, env.tenantBase + "/members/" + userID + "?dry_run=true", memberBody("alice@example.com")},
		{http.MethodPut, env.tenantBase + "/organizations/" + orgID + "/members/" + userID + "?dry_run=true", membershipBody("owner")},
	} {
		rec := call(t, env.handler, c.method, c.target, c.body)
		assertStatus(t, rec, http.StatusBadRequest)
		assertErrorCode(t, rec, "invalid_parameter")
	}

	// The refused request wrote nothing.
	rec := call(t, env.handler, http.MethodGet, env.xoloBase+"/organizations/"+orgID, nil)
	assertStatus(t, rec, http.StatusNotFound)

	// The extensions keep their query parameters.
	rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/organizations?slug=acme", nil)
	assertStatus(t, rec, http.StatusOK)
}

func TestCommonTenants(t *testing.T) {
	t.Run("single-tenant mode refuses to create a tenant", func(t *testing.T) {
		env := newEnv(t)
		tenantID := uuid.NewString()

		rec := call(t, env.handler, http.MethodPut, "/v1/tenants/"+tenantID, resource("acme"))
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")

		if message := decodeBody(t, rec)["error"].(map[string]any)["message"].(string); !strings.Contains(message, "single-tenant") {
			t.Errorf("message should explain the mode, got %q", message)
		}

		rec = call(t, env.handler, http.MethodGet, "/v1/xolo/tenants/"+tenantID, nil)
		assertStatus(t, rec, http.StatusNotFound)
	})

	t.Run("multi-tenant mode creates and updates a tenant", func(t *testing.T) {
		env := newMultiTenantEnv(t)
		tenantID := uuid.NewString()

		rec := call(t, env.handler, http.MethodPut, "/v1/tenants/"+tenantID,
			map[string]any{"slug": "acme", "name": "Acme", "status": "suspended"})
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, env.handler, http.MethodGet, "/v1/xolo/tenants/"+tenantID, nil)
		assertStatus(t, rec, http.StatusOK)
		tenant := decodeBody(t, rec)
		if tenant["id"] != tenantID || tenant["slug"] != "acme" || tenant["active"] != false {
			t.Errorf("tenant: got %v", tenant)
		}

		rec = call(t, env.handler, http.MethodPut, "/v1/tenants/"+tenantID,
			map[string]any{"slug": "acme-corp", "name": "Acme Corporation", "status": "active"})
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, env.handler, http.MethodGet, "/v1/xolo/tenants/"+tenantID, nil)
		tenant = decodeBody(t, rec)
		if tenant["slug"] != "acme-corp" || tenant["name"] != "Acme Corporation" || tenant["active"] != true {
			t.Errorf("tenant: got %v", tenant)
		}
	})

	t.Run("refuses a slug already taken", func(t *testing.T) {
		env := newMultiTenantEnv(t)

		rec := call(t, env.handler, http.MethodPut, "/v1/tenants/"+uuid.NewString(), resource(model.DefaultTenantSlug))
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")
	})

	t.Run("protects the default tenant", func(t *testing.T) {
		env := newMultiTenantEnv(t)

		rec := call(t, env.handler, http.MethodPut, env.tenantBase, resource("renamed"))
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")

		rec = call(t, env.handler, http.MethodPut, env.tenantBase,
			map[string]any{"slug": model.DefaultTenantSlug, "name": "Default", "status": "suspended"})
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")
	})
}

func TestCommonDomains(t *testing.T) {
	env := newMultiTenantEnv(t)

	otherID := uuid.NewString()
	rec := call(t, env.handler, http.MethodPut, "/v1/tenants/"+otherID, resource("other"))
	assertStatus(t, rec, http.StatusOK)

	active := map[string]any{"status": "active"}

	rec = call(t, env.handler, http.MethodPut, env.tenantBase+"/domains/xolo.example.com", active)
	assertStatus(t, rec, http.StatusOK)

	t.Run("a hostname belongs to one tenant", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodPut, "/v1/tenants/"+otherID+"/domains/xolo.example.com", active)
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")
	})

	t.Run("changes the status of a domain", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodPut, env.tenantBase+"/domains/xolo.example.com", map[string]any{"status": "suspended"})
		assertStatus(t, rec, http.StatusOK)
		if status := decodeBody(t, rec)["status"]; status != "suspended" {
			t.Errorf("status: got %v", status)
		}
	})

	t.Run("refuses a non-canonical hostname", func(t *testing.T) {
		for _, hostname := range []string{"Xolo.example.com", "XOLO.EXAMPLE.COM", "127.0.0.1", "under_score.example.com", "-dash.example.com"} {
			rec := call(t, env.handler, http.MethodPut, env.tenantBase+"/domains/"+hostname, active)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: got %d, want 400 (body: %s)", hostname, rec.Code, rec.Body.String())
				continue
			}
			assertErrorCode(t, rec, "invalid_hostname")
		}
	})

	t.Run("refuses an invalid status", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodPut, env.tenantBase+"/domains/new.example.com", map[string]any{"status": "deleted"})
		assertStatus(t, rec, http.StatusUnprocessableEntity)
		assertErrorCode(t, rec, "unprocessable")
	})

	t.Run("requires an existing tenant", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodPut, "/v1/tenants/"+uuid.NewString()+"/domains/new.example.com", active)
		assertStatus(t, rec, http.StatusNotFound)
		assertErrorCode(t, rec, "parent_not_found")
	})
}

func TestCommonParents(t *testing.T) {
	env := newMultiTenantEnv(t)

	otherID := uuid.NewString()
	rec := call(t, env.handler, http.MethodPut, "/v1/tenants/"+otherID, resource("other"))
	assertStatus(t, rec, http.StatusOK)

	orgID := putOrganization(t, env.handler, env.tenantID, "acme")
	userID := putMember(t, env.handler, env.tenantID, "jane@acme.tld")
	otherOrgID := putOrganization(t, env.handler, otherID, "acme")
	otherUserID := putMember(t, env.handler, otherID, "john@other.tld")

	unknown := uuid.NewString()

	parentNotFound := map[string]struct {
		target  string
		body    map[string]any
		message string
	}{
		"organization of an unknown tenant":   {"/v1/tenants/" + unknown + "/organizations/" + uuid.NewString(), resource("acme"), "tenant not found"},
		"member of an unknown tenant":         {"/v1/tenants/" + unknown + "/members/" + uuid.NewString(), memberBody("x@acme.tld"), "tenant not found"},
		"membership of an unknown tenant":     {"/v1/tenants/" + unknown + "/organizations/" + orgID + "/members/" + userID, membershipBody("member"), "tenant not found"},
		"membership of an unknown org":        {env.tenantBase + "/organizations/" + unknown + "/members/" + userID, membershipBody("member"), "organization not found"},
		"membership of an unknown member":     {env.tenantBase + "/organizations/" + orgID + "/members/" + unknown, membershipBody("member"), "user not found"},
		"membership of a foreign org":         {env.tenantBase + "/organizations/" + otherOrgID + "/members/" + userID, membershipBody("member"), "organization not found"},
		"membership of a foreign member":      {env.tenantBase + "/organizations/" + orgID + "/members/" + otherUserID, membershipBody("member"), "user not found"},
		"org of a tenant reached by other id": {"/v1/tenants/" + otherID + "/organizations/" + orgID + "/members/" + otherUserID, membershipBody("member"), "organization not found"},
	}

	for name, request := range parentNotFound {
		t.Run(name, func(t *testing.T) {
			rec := call(t, env.handler, http.MethodPut, request.target, request.body)
			assertStatus(t, rec, http.StatusNotFound)
			assertErrorCode(t, rec, "parent_not_found")

			if message := decodeBody(t, rec)["error"].(map[string]any)["message"].(string); message != request.message {
				t.Errorf("message: got %q, want %q", message, request.message)
			}
		})
	}

	t.Run("a global id used by another tenant is a conflict", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodPut, "/v1/tenants/"+otherID+"/organizations/"+orgID, resource("stolen"))
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")

		rec = call(t, env.handler, http.MethodPut, "/v1/tenants/"+otherID+"/members/"+userID, memberBody("stolen@other.tld"))
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")

		// Nothing moved.
		rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/organizations/"+orgID, nil)
		assertStatus(t, rec, http.StatusOK)
		if slug := decodeBody(t, rec)["slug"]; slug != "acme" {
			t.Errorf("slug: got %v", slug)
		}

		rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/users/"+userID, nil)
		assertStatus(t, rec, http.StatusOK)
		if email := decodeBody(t, rec)["email"]; email != "jane@acme.tld" {
			t.Errorf("email: got %v", email)
		}
	})

	t.Run("an unknown member is not created", func(t *testing.T) {
		memberID := uuid.NewString()

		rec := call(t, env.handler, http.MethodPut, env.tenantBase+"/members/"+memberID, memberBody("new@acme.tld"))
		assertStatus(t, rec, http.StatusNotFound)
		assertErrorCode(t, rec, "not_found")

		rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/users/"+memberID, nil)
		assertStatus(t, rec, http.StatusNotFound)
	})

	t.Run("a duplicate slug within the tenant is a conflict", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodPut, env.tenantBase+"/organizations/"+uuid.NewString(), resource("acme"))
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")
	})
}

func TestCommonLastOwner(t *testing.T) {
	t.Run("organization", func(t *testing.T) {
		env := newEnv(t)

		orgID := putOrganization(t, env.handler, env.tenantID, "acme")
		ownerID, _ := addOrgMember(t, env.handler, env.tenantID, orgID, "owner@acme.tld", "owner")
		ownerPath := env.tenantBase + "/organizations/" + orgID + "/members/" + ownerID

		for _, body := range []map[string]any{
			membershipBody("admin"),
			membershipBody("member"),
			{"role": "owner", "status": "suspended"},
		} {
			rec := call(t, env.handler, http.MethodPut, ownerPath, body)
			assertStatus(t, rec, http.StatusConflict)
			assertErrorCode(t, rec, "last_owner")
		}

		// With a second owner, the first one may step down.
		addOrgMember(t, env.handler, env.tenantID, orgID, "second@acme.tld", "owner")

		rec := call(t, env.handler, http.MethodPut, ownerPath, membershipBody("member"))
		assertStatus(t, rec, http.StatusOK)
	})

	t.Run("tenant", func(t *testing.T) {
		env := newEnv(t)

		ownerID := createUser(t, env.handler, env.tenantID, "sub-owner")
		ownerPath := env.tenantBase + "/members/" + ownerID

		rec := call(t, env.handler, http.MethodPut, ownerPath,
			map[string]any{"email": "owner@acme.tld", "tenant_role": "owner", "status": "active"})
		assertStatus(t, rec, http.StatusOK)

		for _, body := range []map[string]any{
			{"email": "owner@acme.tld", "tenant_role": "member", "status": "active"},
			{"email": "owner@acme.tld", "tenant_role": "owner", "status": "suspended"},
		} {
			rec = call(t, env.handler, http.MethodPut, ownerPath, body)
			assertStatus(t, rec, http.StatusConflict)
			assertErrorCode(t, rec, "last_owner")
		}

		rec = call(t, env.handler, http.MethodPut, env.tenantBase+"/members/"+createUser(t, env.handler, env.tenantID, "sub-second"),
			map[string]any{"email": "second@acme.tld", "tenant_role": "owner", "status": "active"})
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, env.handler, http.MethodPut, ownerPath,
			map[string]any{"email": "owner@acme.tld", "tenant_role": "member", "status": "active"})
		assertStatus(t, rec, http.StatusOK)
	})
}

func TestCommonPlatformAdminProtection(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()

	found, err := env.store.FindOrCreateUser(ctx, model.TenantID(env.tenantID), "openid-connect", "sub-admin")
	if err != nil {
		t.Fatalf("find or create user: %v", err)
	}

	admin := model.CopyUser(found)
	admin.SetEmail("admin@acme.tld")
	admin.SetDisplayName("Admin")
	admin.SetActive(true)
	admin.SetRoles(model.PlatformRoleUser, model.PlatformRoleAdmin)

	if err := env.store.SaveUser(ctx, admin); err != nil {
		t.Fatalf("save user: %v", err)
	}

	adminPath := env.tenantBase + "/members/" + string(admin.ID())
	current := map[string]any{
		"email":        "admin@acme.tld",
		"display_name": "Admin",
		"tenant_role":  string(admin.TenantRole()),
		"status":       "active",
	}

	changes := map[string]map[string]any{
		"email":        {"email": "evil@acme.tld"},
		"display name": {"display_name": "Renamed"},
		"status":       {"status": "suspended"},
		"tenant role":  {"tenant_role": "owner"},
	}

	for name, change := range changes {
		t.Run("refuses to change the "+name, func(t *testing.T) {
			body := map[string]any{}
			for key, value := range current {
				body[key] = value
			}
			for key, value := range change {
				body[key] = value
			}

			rec := call(t, env.handler, http.MethodPut, adminPath, body)
			assertStatus(t, rec, http.StatusConflict)
			assertErrorCode(t, rec, "platform_admin_protected")
		})
	}

	t.Run("accepts the identical representation", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodPut, adminPath, current)
		assertStatus(t, rec, http.StatusOK)
	})

	rec := call(t, env.handler, http.MethodGet, env.xoloBase+"/users/"+string(admin.ID()), nil)
	assertStatus(t, rec, http.StatusOK)
	user := decodeBody(t, rec)
	if user["email"] != "admin@acme.tld" || user["displayName"] != "Admin" || user["active"] != true {
		t.Errorf("the platform admin should be untouched, got %v", user)
	}
}

func TestCommonMemberKeepsIdentity(t *testing.T) {
	env := newEnv(t)

	rec := call(t, env.handler, http.MethodPut, env.xoloBase+"/users", map[string]any{
		"provider":    "openid-connect",
		"subject":     "sub-linked",
		"email":       "linked@acme.tld",
		"displayName": "Linked",
	})
	assertStatus(t, rec, http.StatusCreated)
	userID := decodeBody(t, rec)["id"].(string)

	rec = call(t, env.handler, http.MethodPut, env.tenantBase+"/members/"+userID, map[string]any{
		"email":        "renamed@acme.tld",
		"display_name": "Renamed",
		"tenant_role":  "member",
		"status":       "active",
	})
	assertStatus(t, rec, http.StatusOK)

	rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/users/"+userID, nil)
	assertStatus(t, rec, http.StatusOK)

	user := decodeBody(t, rec)
	if user["provider"] != "openid-connect" || user["subject"] != "sub-linked" {
		t.Errorf("identity should be preserved, got %v", user)
	}
	if user["email"] != "renamed@acme.tld" || user["displayName"] != "Renamed" {
		t.Errorf("common fields should be applied, got %v", user)
	}

	// The identity still resolves to the same user.
	rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/users?provider=openid-connect&subject=sub-linked", nil)
	assertStatus(t, rec, http.StatusOK)
	items, _ := decodeBody(t, rec)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != userID {
		t.Errorf("identity lookup: got %v", items)
	}
}

// ── Xolo extensions ─────────────────────────────────────────────────────────

func TestHealthzAndPermissions(t *testing.T) {
	env := newEnv(t)

	rec := call(t, env.handler, http.MethodGet, "/v1/xolo/healthz", nil)
	assertStatus(t, rec, http.StatusOK)

	rec = call(t, env.handler, http.MethodGet, "/v1/xolo/permissions", nil)
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
	t.Run("reads and updates an organization", func(t *testing.T) {
		env := newEnv(t)
		orgBase := env.xoloBase + "/organizations"

		orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", false)

		rec := call(t, env.handler, http.MethodGet, orgBase+"/"+orgID, nil)
		assertStatus(t, rec, http.StatusOK)
		if id := decodeBody(t, rec)["id"]; id != orgID {
			t.Errorf("id: got %v, want %q", id, orgID)
		}

		rec = call(t, env.handler, http.MethodPatch, orgBase+"/"+orgID, map[string]any{"name": "Acme Corporation", "currency": "USD"})
		assertStatus(t, rec, http.StatusOK)
		body := decodeBody(t, rec)
		if body["name"] != "Acme Corporation" || body["currency"] != "USD" {
			t.Errorf("organization: got %v", body)
		}

		rec = call(t, env.handler, http.MethodGet, orgBase+"/"+uuid.NewString(), nil)
		assertStatus(t, rec, http.StatusNotFound)
		assertErrorCode(t, rec, "not_found")
	})

	t.Run("rejects invalid payloads", func(t *testing.T) {
		env := newEnv(t)
		orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", false)
		target := env.xoloBase + "/organizations/" + orgID

		cases := map[string]struct {
			body   any
			status int
			code   string
		}{
			"malformed json": {"{", http.StatusBadRequest, "invalid_request"},
			"unknown field":  {map[string]any{"name": "Acme", "nope": true}, http.StatusBadRequest, "invalid_request"},
			"slug":           {map[string]any{"slug": "renamed"}, http.StatusBadRequest, "invalid_request"},
			"empty name":     {map[string]any{"name": ""}, http.StatusUnprocessableEntity, "unprocessable"},
			"bad currency":   {map[string]any{"currency": "XXX"}, http.StatusUnprocessableEntity, "unprocessable"},
		}

		for name, testCase := range cases {
			t.Run(name, func(t *testing.T) {
				rec := call(t, env.handler, http.MethodPatch, target, testCase.body)
				assertStatus(t, rec, testCase.status)
				assertErrorCode(t, rec, testCase.code)
			})
		}
	})

	t.Run("looks an organization up by slug", func(t *testing.T) {
		env := newEnv(t)
		orgBase := env.xoloBase + "/organizations"

		orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", false)

		rec := call(t, env.handler, http.MethodGet, orgBase+"?slug=acme", nil)
		assertStatus(t, rec, http.StatusOK)
		body := decodeBody(t, rec)
		if total := body["total"]; total != float64(1) {
			t.Errorf("total: got %v, want 1", total)
		}
		if id := body["items"].([]any)[0].(map[string]any)["id"]; id != orgID {
			t.Errorf("id: got %v, want %q", id, orgID)
		}

		rec = call(t, env.handler, http.MethodGet, orgBase+"?slug=unknown", nil)
		assertStatus(t, rec, http.StatusOK)
		if total := decodeBody(t, rec)["total"]; total != float64(0) {
			t.Errorf("total: got %v, want 0", total)
		}
	})

	t.Run("lists organizations", func(t *testing.T) {
		env := newEnv(t)

		createOrganization(t, env.handler, env.tenantID, "acme", false)
		createOrganization(t, env.handler, env.tenantID, "other", false)

		rec := call(t, env.handler, http.MethodGet, env.xoloBase+"/organizations?limit=1", nil)
		assertStatus(t, rec, http.StatusOK)
		body := decodeBody(t, rec)
		if body["total"] != float64(2) || len(body["items"].([]any)) != 1 {
			t.Errorf("page: got %v", body)
		}
	})

	t.Run("rejects invalid pagination", func(t *testing.T) {
		env := newEnv(t)

		rec := call(t, env.handler, http.MethodGet, env.xoloBase+"/organizations?limit=10000", nil)
		assertStatus(t, rec, http.StatusBadRequest)
		assertErrorCode(t, rec, "invalid_request")
	})
}

func TestMemberEndpoints(t *testing.T) {
	t.Run("lists, reads and updates the roles of a member", func(t *testing.T) {
		env := newEnv(t)
		orgBase := env.xoloBase + "/organizations"

		orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", true)
		userID, membershipID := addOrgMember(t, env.handler, env.tenantID, orgID, "member@acme.tld", "member")

		rec := call(t, env.handler, http.MethodGet, orgBase+"/"+orgID+"/members/"+membershipID, nil)
		assertStatus(t, rec, http.StatusOK)
		body := decodeBody(t, rec)
		if body["userId"] != userID || body["organizationId"] != orgID {
			t.Errorf("membership: got %v", body)
		}

		rec = call(t, env.handler, http.MethodPut, orgBase+"/"+orgID+"/members/"+membershipID+"/roles",
			map[string]any{"builtinRoles": []string{"admin"}})
		assertStatus(t, rec, http.StatusOK)

		roles := decodeBody(t, rec)["roles"].([]any)
		if len(roles) != 1 || roles[0].(map[string]any)["builtinKind"] != "admin" {
			t.Errorf("roles: got %v", roles)
		}

		rec = call(t, env.handler, http.MethodGet, orgBase+"/"+orgID+"/members", nil)
		assertStatus(t, rec, http.StatusOK)
		if total := decodeBody(t, rec)["total"]; total != float64(2) {
			t.Errorf("total: got %v, want 2", total)
		}

		// The common role reflects the change made through the extension:
		// replaying it is a no-op, not a conflict.
		rec = call(t, env.handler, http.MethodPut, env.tenantBase+"/organizations/"+orgID+"/members/"+userID, membershipBody("admin"))
		assertStatus(t, rec, http.StatusOK)
	})

	t.Run("keeps custom roles when the common role changes", func(t *testing.T) {
		env := newEnv(t)
		orgBase := env.xoloBase + "/organizations"

		orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", true)
		userID, membershipID := addOrgMember(t, env.handler, env.tenantID, orgID, "member@acme.tld", "member")

		rec := call(t, env.handler, http.MethodPost, orgBase+"/"+orgID+"/roles", map[string]any{"name": "auditor"})
		assertStatus(t, rec, http.StatusCreated)
		customID := decodeBody(t, rec)["id"].(string)

		rec = call(t, env.handler, http.MethodPut, orgBase+"/"+orgID+"/members/"+membershipID+"/roles",
			map[string]any{"builtinRoles": []string{"member"}, "roleIds": []string{customID}})
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, env.handler, http.MethodPut, env.tenantBase+"/organizations/"+orgID+"/members/"+userID, membershipBody("admin"))
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, env.handler, http.MethodGet, orgBase+"/"+orgID+"/members/"+membershipID, nil)
		assertStatus(t, rec, http.StatusOK)

		kinds := map[string]bool{}
		for _, raw := range decodeBody(t, rec)["roles"].([]any) {
			role := raw.(map[string]any)
			if role["id"] == customID {
				kinds["custom"] = true
			}
			if kind, _ := role["builtinKind"].(string); kind != "" {
				kinds[kind] = true
			}
		}
		if !kinds["custom"] || !kinds["admin"] || kinds["member"] {
			t.Errorf("roles: got %v, want the custom role and admin only", kinds)
		}
	})

	t.Run("refuses a role belonging to another organization", func(t *testing.T) {
		env := newEnv(t)
		orgBase := env.xoloBase + "/organizations"

		acmeID, _ := createOrganization(t, env.handler, env.tenantID, "acme", true)
		otherID, _ := createOrganization(t, env.handler, env.tenantID, "other", false)

		rec := call(t, env.handler, http.MethodGet, orgBase+"/"+otherID+"/roles", nil)
		assertStatus(t, rec, http.StatusOK)
		otherRoleID := decodeBody(t, rec)["items"].([]any)[0].(map[string]any)["id"].(string)

		_, membershipID := addOrgMember(t, env.handler, env.tenantID, acmeID, "member@acme.tld", "member")

		rec = call(t, env.handler, http.MethodPut, orgBase+"/"+acmeID+"/members/"+membershipID+"/roles",
			map[string]any{"roleIds": []string{otherRoleID}})
		assertStatus(t, rec, http.StatusUnprocessableEntity)
		assertErrorCode(t, rec, "unprocessable")
	})

	t.Run("refuses to drop the last owner", func(t *testing.T) {
		env := newEnv(t)
		orgBase := env.xoloBase + "/organizations"

		orgID, ownerMembershipID := createOrganization(t, env.handler, env.tenantID, "acme", true)

		rec := call(t, env.handler, http.MethodPut, orgBase+"/"+orgID+"/members/"+ownerMembershipID+"/roles",
			map[string]any{"builtinRoles": []string{"member"}})
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "last_owner")
	})

	t.Run("hides resources of another organization", func(t *testing.T) {
		env := newEnv(t)
		orgBase := env.xoloBase + "/organizations"

		acmeID, acmeMembershipID := createOrganization(t, env.handler, env.tenantID, "acme", true)
		otherID, _ := createOrganization(t, env.handler, env.tenantID, "other", false)

		rec := call(t, env.handler, http.MethodGet, orgBase+"/"+otherID+"/members/"+acmeMembershipID, nil)
		assertStatus(t, rec, http.StatusNotFound)
		assertErrorCode(t, rec, "not_found")

		rec = call(t, env.handler, http.MethodGet, orgBase+"/"+acmeID+"/members/nope", nil)
		assertStatus(t, rec, http.StatusNotFound)

		rec = call(t, env.handler, http.MethodGet, orgBase+"/nope/members", nil)
		assertStatus(t, rec, http.StatusNotFound)
	})
}

func TestRoleEndpoints(t *testing.T) {
	t.Run("creates, updates and deletes a custom role", func(t *testing.T) {
		env := newEnv(t)
		orgBase := env.xoloBase + "/organizations"

		orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", false)

		rec := call(t, env.handler, http.MethodPost, orgBase+"/"+orgID+"/roles", map[string]any{
			"name":        "auditor",
			"permissions": []string{"usage:read"},
		})
		assertStatus(t, rec, http.StatusCreated)

		roleID := decodeBody(t, rec)["id"].(string)

		rec = call(t, env.handler, http.MethodGet, orgBase+"/"+orgID+"/roles/"+roleID, nil)
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, env.handler, http.MethodPut, orgBase+"/"+orgID+"/roles/"+roleID, map[string]any{
			"permissions": []string{"usage:read", "members:read"},
		})
		assertStatus(t, rec, http.StatusOK)
		if permissions := decodeBody(t, rec)["permissions"].([]any); len(permissions) != 2 {
			t.Errorf("permissions: got %v", permissions)
		}

		rec = call(t, env.handler, http.MethodDelete, orgBase+"/"+orgID+"/roles/"+roleID, nil)
		assertStatus(t, rec, http.StatusNoContent)

		rec = call(t, env.handler, http.MethodGet, orgBase+"/"+orgID+"/roles/"+roleID, nil)
		assertStatus(t, rec, http.StatusNotFound)
	})

	t.Run("refuses an unknown permission", func(t *testing.T) {
		env := newEnv(t)
		orgBase := env.xoloBase + "/organizations"

		orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", false)

		rec := call(t, env.handler, http.MethodPost, orgBase+"/"+orgID+"/roles", map[string]any{
			"name":        "bogus",
			"permissions": []string{"not:a:permission"},
		})
		assertStatus(t, rec, http.StatusUnprocessableEntity)
		assertErrorCode(t, rec, "unprocessable")
	})

	t.Run("refuses a duplicate role name", func(t *testing.T) {
		env := newEnv(t)
		orgBase := env.xoloBase + "/organizations"

		orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", false)

		rec := call(t, env.handler, http.MethodPost, orgBase+"/"+orgID+"/roles", map[string]any{"name": "auditor"})
		assertStatus(t, rec, http.StatusCreated)

		rec = call(t, env.handler, http.MethodPost, orgBase+"/"+orgID+"/roles", map[string]any{"name": "auditor"})
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")
	})

	t.Run("protects builtin roles", func(t *testing.T) {
		env := newEnv(t)
		orgBase := env.xoloBase + "/organizations"

		orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", false)

		rec := call(t, env.handler, http.MethodGet, orgBase+"/"+orgID+"/roles", nil)
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

		rec = call(t, env.handler, http.MethodPut, orgBase+"/"+orgID+"/roles/"+builtinID, map[string]any{"name": "hacked"})
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")

		rec = call(t, env.handler, http.MethodDelete, orgBase+"/"+orgID+"/roles/"+builtinID, nil)
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
		env := newEnv(t)

		rec := call(t, env.handler, http.MethodPut, env.xoloBase+"/users", identity)
		assertStatus(t, rec, http.StatusCreated)
		userID := decodeBody(t, rec)["id"].(string)

		rec = call(t, env.handler, http.MethodPut, env.xoloBase+"/users", identity)
		assertStatus(t, rec, http.StatusOK)
		if id := decodeBody(t, rec)["id"]; id != userID {
			t.Errorf("id: got %v, want %q", id, userID)
		}
	})

	t.Run("looks a user up by identity", func(t *testing.T) {
		env := newEnv(t)

		call(t, env.handler, http.MethodPut, env.xoloBase+"/users", identity)

		rec := call(t, env.handler, http.MethodGet, env.xoloBase+"/users?provider=openid-connect&subject=sub-1", nil)
		assertStatus(t, rec, http.StatusOK)
		if total := decodeBody(t, rec)["total"]; total != float64(1) {
			t.Errorf("total: got %v, want 1", total)
		}

		rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/users?provider=openid-connect&subject=unknown", nil)
		assertStatus(t, rec, http.StatusOK)
		if total := decodeBody(t, rec)["total"]; total != float64(0) {
			t.Errorf("total: got %v, want 0", total)
		}

		rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/users?provider=openid-connect", nil)
		assertStatus(t, rec, http.StatusBadRequest)
		assertErrorCode(t, rec, "invalid_request")
	})

	t.Run("reads a user", func(t *testing.T) {
		env := newEnv(t)

		rec := call(t, env.handler, http.MethodPut, env.xoloBase+"/users", identity)
		userID := decodeBody(t, rec)["id"].(string)

		rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/users/"+userID, nil)
		assertStatus(t, rec, http.StatusOK)
		if name := decodeBody(t, rec)["displayName"]; name != "User" {
			t.Errorf("display name: got %v", name)
		}

		rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/users/nope", nil)
		assertStatus(t, rec, http.StatusNotFound)
	})

	t.Run("filters on the active flag", func(t *testing.T) {
		env := newEnv(t)

		active := false
		call(t, env.handler, http.MethodPut, env.xoloBase+"/users", identity)
		call(t, env.handler, http.MethodPut, env.xoloBase+"/users", map[string]any{
			"provider": "openid-connect",
			"subject":  "sub-pending",
			"email":    "pending@acme.tld",
			"active":   active,
		})

		rec := call(t, env.handler, http.MethodGet, env.xoloBase+"/users?active=false", nil)
		assertStatus(t, rec, http.StatusOK)

		items := decodeBody(t, rec)["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("inactive users: got %d, want 1", len(items))
		}
		if email := items[0].(map[string]any)["email"]; email != "pending@acme.tld" {
			t.Errorf("email: got %v", email)
		}

		rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/users?active=true", nil)
		assertStatus(t, rec, http.StatusOK)
		if total := decodeBody(t, rec)["total"]; total != float64(1) {
			t.Errorf("active users: got %v, want 1", total)
		}

		rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/users?active=maybe", nil)
		assertStatus(t, rec, http.StatusBadRequest)
		assertErrorCode(t, rec, "invalid_request")
	})

	t.Run("never exposes platform role management", func(t *testing.T) {
		env := newEnv(t)

		withRoles := map[string]any{"platformRoles": []string{"admin"}}
		for key, value := range identity {
			withRoles[key] = value
		}

		rec := call(t, env.handler, http.MethodPut, env.xoloBase+"/users", withRoles)
		assertStatus(t, rec, http.StatusBadRequest)
		assertErrorCode(t, rec, "invalid_request")

		rec = call(t, env.handler, http.MethodPut, env.xoloBase+"/users", identity)
		userID := decodeBody(t, rec)["id"].(string)

		rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/users/"+userID, nil)
		roles := decodeBody(t, rec)["platformRoles"].([]any)
		if len(roles) != 1 || roles[0] != "user" {
			t.Errorf("platform roles: got %v, want [user]", roles)
		}
	})
}

func TestTenantEndpoints(t *testing.T) {
	t.Run("single-tenant mode still exposes its tenant", func(t *testing.T) {
		env := newEnv(t)

		// A control plane discovers the identifier every nested route needs
		// through the slug lookup, without any tenant having been created.
		rec := call(t, env.handler, http.MethodGet, "/v1/xolo/tenants?slug="+model.DefaultTenantSlug, nil)
		assertStatus(t, rec, http.StatusOK)

		items, _ := decodeBody(t, rec)["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("items: got %d, want 1", len(items))
		}
		if id, _ := items[0].(map[string]any)["id"].(string); id != env.tenantID {
			t.Errorf("id %q should be the one every path is built from (%q)", id, env.tenantID)
		}

		rec = call(t, env.handler, http.MethodGet, "/v1/xolo/tenants", nil)
		assertStatus(t, rec, http.StatusOK)
		if total := decodeBody(t, rec)["total"]; total != float64(1) {
			t.Errorf("total: got %v, want 1", total)
		}
	})

	t.Run("reads and updates a tenant", func(t *testing.T) {
		env := newMultiTenantEnv(t)
		tenantID := uuid.NewString()

		rec := call(t, env.handler, http.MethodPut, "/v1/tenants/"+tenantID, resource("acme"))
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, env.handler, http.MethodGet, "/v1/xolo/tenants/"+tenantID, nil)
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, env.handler, http.MethodPatch, "/v1/xolo/tenants/"+tenantID,
			map[string]any{"name": "Acme Corporation", "description": "Acme Inc."})
		assertStatus(t, rec, http.StatusOK)
		body := decodeBody(t, rec)
		if body["name"] != "Acme Corporation" || body["description"] != "Acme Inc." {
			t.Errorf("tenant: got %v", body)
		}

		// The common PUT governs neither the description nor anything else
		// outside its representation.
		rec = call(t, env.handler, http.MethodPut, "/v1/tenants/"+tenantID,
			map[string]any{"slug": "acme", "name": "Acme", "status": "active"})
		assertStatus(t, rec, http.StatusOK)

		rec = call(t, env.handler, http.MethodGet, "/v1/xolo/tenants/"+tenantID, nil)
		if description := decodeBody(t, rec)["description"]; description != "Acme Inc." {
			t.Errorf("description: got %v", description)
		}
	})

	t.Run("protects the default tenant", func(t *testing.T) {
		env := newMultiTenantEnv(t)

		rec := call(t, env.handler, http.MethodPatch, env.xoloBase, map[string]any{"active": false})
		assertStatus(t, rec, http.StatusConflict)
	})

	t.Run("rejects invalid payloads", func(t *testing.T) {
		env := newEnv(t)

		cases := map[string]struct {
			body   any
			status int
			code   string
		}{
			"malformed json": {"{", http.StatusBadRequest, "invalid_request"},
			"unknown field":  {map[string]any{"name": "Acme", "currency": "EUR"}, http.StatusBadRequest, "invalid_request"},
			"slug":           {map[string]any{"slug": "renamed"}, http.StatusBadRequest, "invalid_request"},
			"empty name":     {map[string]any{"name": ""}, http.StatusUnprocessableEntity, "unprocessable"},
		}

		for name, testCase := range cases {
			t.Run(name, func(t *testing.T) {
				rec := call(t, env.handler, http.MethodPatch, env.xoloBase, testCase.body)
				assertStatus(t, rec, testCase.status)
				assertErrorCode(t, rec, testCase.code)
			})
		}
	})

	t.Run("hides the resources of another tenant", func(t *testing.T) {
		env := newMultiTenantEnv(t)

		otherTenantID := uuid.NewString()
		rec := call(t, env.handler, http.MethodPut, "/v1/tenants/"+otherTenantID, resource("acme"))
		assertStatus(t, rec, http.StatusOK)

		// An organization of the default tenant must not be reachable through
		// another tenant's path, even with the right identifier.
		orgID, membershipID := createOrganization(t, env.handler, env.tenantID, "acme", true)

		otherBase := "/v1/xolo/tenants/" + otherTenantID + "/organizations"

		for _, target := range []string{
			otherBase + "/" + orgID,
			otherBase + "/" + orgID + "/members",
			otherBase + "/" + orgID + "/members/" + membershipID,
			otherBase + "/" + orgID + "/roles",
		} {
			rec = call(t, env.handler, http.MethodGet, target, nil)
			assertStatus(t, rec, http.StatusNotFound)
		}

		rec = call(t, env.handler, http.MethodPatch, otherBase+"/"+orgID, map[string]any{"name": "Hijacked"})
		assertStatus(t, rec, http.StatusNotFound)

		// The same slug is free on the other tenant: isolation, not collision.
		putOrganization(t, env.handler, otherTenantID, "acme")
	})

	t.Run("an unknown tenant is reported as not found", func(t *testing.T) {
		env := newEnv(t)

		for _, target := range []string{
			"/v1/xolo/tenants/nope",
			"/v1/xolo/tenants/nope/organizations",
			"/v1/xolo/tenants/nope/users",
			"/v1/xolo/tenants/" + uuid.NewString(),
		} {
			rec := call(t, env.handler, http.MethodGet, target, nil)
			assertStatus(t, rec, http.StatusNotFound)
			assertErrorCode(t, rec, "not_found")
		}
	})
}

// ── Routing ─────────────────────────────────────────────────────────────────

func TestRoutingFallbacks(t *testing.T) {
	env := newEnv(t)

	for _, target := range []string{"/v1/unknown", "/v1/xolo/unknown", "/v2/manifest"} {
		rec := call(t, env.handler, http.MethodGet, target, nil)
		assertStatus(t, rec, http.StatusNotFound)
		assertErrorCode(t, rec, "not_found")
	}

	rec := call(t, env.handler, http.MethodDelete, "/v1/xolo/permissions", nil)
	assertStatus(t, rec, http.StatusMethodNotAllowed)
	assertErrorCode(t, rec, "method_not_allowed")
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Errorf("Allow: got %q, want GET", allow)
	}

	rec = call(t, env.handler, http.MethodPost, "/v1/manifest", nil)
	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

// TestRemovedRoutes checks that the routes the common contract replaced, and
// the extensions' former un-prefixed paths, no longer act.
func TestRemovedRoutes(t *testing.T) {
	env := newMultiTenantEnv(t)

	orgID, membershipID := createOrganization(t, env.handler, env.tenantID, "acme", true)
	userID, _ := addOrgMember(t, env.handler, env.tenantID, orgID, "member@acme.tld", "member")

	orgPath := "/organizations/" + orgID
	identity := map[string]any{"provider": "openid-connect", "subject": "sub-removed", "email": "removed@acme.tld"}

	removed := []struct {
		method string
		target string
		body   any
	}{
		// Replaced by the common PUTs, under both prefixes.
		{http.MethodPost, "/v1/tenants", map[string]any{"slug": "created", "name": "Created"}},
		{http.MethodPost, "/v1/xolo/tenants", map[string]any{"slug": "created", "name": "Created"}},
		{http.MethodDelete, env.tenantBase, nil},
		{http.MethodDelete, env.xoloBase, nil},
		{http.MethodPost, env.tenantBase + "/organizations", map[string]any{"slug": "created", "name": "Created"}},
		{http.MethodPost, env.xoloBase + "/organizations", map[string]any{"slug": "created", "name": "Created"}},
		{http.MethodDelete, env.tenantBase + orgPath, nil},
		{http.MethodDelete, env.xoloBase + orgPath, nil},
		{http.MethodPost, env.tenantBase + orgPath + "/members", map[string]any{"user": identity, "builtinRoles": []string{"member"}}},
		{http.MethodPost, env.xoloBase + orgPath + "/members", map[string]any{"user": identity, "builtinRoles": []string{"member"}}},
		{http.MethodDelete, env.tenantBase + orgPath + "/members/" + membershipID, nil},
		{http.MethodDelete, env.xoloBase + orgPath + "/members/" + membershipID, nil},
		{http.MethodDelete, env.tenantBase + orgPath + "/members/" + userID, nil},
		{http.MethodPatch, env.tenantBase + "/users/" + userID, map[string]any{"displayName": "Renamed"}},
		{http.MethodPatch, env.xoloBase + "/users/" + userID, map[string]any{"displayName": "Renamed"}},

		// Former un-prefixed extension paths. Their GETs on tenants,
		// organizations and members are now the common reads.
		{http.MethodGet, "/v1/healthz", nil},
		{http.MethodGet, "/v1/permissions", nil},
		{http.MethodPatch, env.tenantBase, map[string]any{"name": "Renamed"}},
		{http.MethodPatch, env.tenantBase + orgPath, map[string]any{"name": "Renamed"}},
		{http.MethodPut, env.tenantBase + orgPath + "/members/" + membershipID + "/roles", map[string]any{"builtinRoles": []string{"member"}}},
		{http.MethodGet, env.tenantBase + orgPath + "/roles", nil},
		{http.MethodPost, env.tenantBase + orgPath + "/roles", map[string]any{"name": "auditor"}},
		{http.MethodGet, env.tenantBase + "/users", nil},
		{http.MethodPut, env.tenantBase + "/users", identity},
		{http.MethodGet, env.tenantBase + "/users/" + userID, nil},
	}

	for _, route := range removed {
		t.Run(route.method+" "+route.target, func(t *testing.T) {
			rec := call(t, env.handler, route.method, route.target, route.body)

			switch rec.Code {
			case http.StatusNotFound:
				assertErrorCode(t, rec, "not_found")
			case http.StatusMethodNotAllowed:
				assertErrorCode(t, rec, "method_not_allowed")
				if strings.Contains(rec.Header().Get("Allow"), route.method) {
					t.Errorf("Allow %q should not list %s", rec.Header().Get("Allow"), route.method)
				}
			default:
				t.Fatalf("got %d, want 404 or 405 (body: %s)", rec.Code, rec.Body.String())
			}
		})
	}

	// None of them acted.
	rec := call(t, env.handler, http.MethodGet, "/v1/xolo/tenants", nil)
	assertStatus(t, rec, http.StatusOK)
	if total := decodeBody(t, rec)["total"]; total != float64(1) {
		t.Errorf("tenants: got %v, want 1", total)
	}

	rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/organizations", nil)
	assertStatus(t, rec, http.StatusOK)
	if total := decodeBody(t, rec)["total"]; total != float64(1) {
		t.Errorf("organizations: got %v, want 1", total)
	}

	rec = call(t, env.handler, http.MethodGet, env.xoloBase+orgPath+"/members", nil)
	assertStatus(t, rec, http.StatusOK)
	if total := decodeBody(t, rec)["total"]; total != float64(2) {
		t.Errorf("members: got %v, want 2", total)
	}

	rec = call(t, env.handler, http.MethodGet, env.xoloBase+orgPath+"/roles", nil)
	assertStatus(t, rec, http.StatusOK)
	if total := decodeBody(t, rec)["total"]; total != float64(3) {
		t.Errorf("roles: got %v, want the 3 builtin roles", total)
	}

	rec = call(t, env.handler, http.MethodGet, env.xoloBase+"/users", nil)
	assertStatus(t, rec, http.StatusOK)
	if total := decodeBody(t, rec)["total"]; total != float64(2) {
		t.Errorf("users: got %v, want 2", total)
	}
}

// TestInternalErrorsDoNotLeak checks that an unexpected failure is reported as
// a generic error: no SQL, no file path, no stack trace.
func TestInternalErrorsDoNotLeak(t *testing.T) {
	env := newEnv(t)

	createOrganization(t, env.handler, env.tenantID, "acme", false)

	sqlDB, err := env.db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	for _, request := range []struct {
		method string
		target string
		body   any
	}{
		{http.MethodGet, env.xoloBase + "/organizations", nil},
		{http.MethodPut, env.tenantBase + "/organizations/" + uuid.NewString(), resource("other")},
	} {
		rec := call(t, env.handler, request.method, request.target, request.body)
		assertStatus(t, rec, http.StatusInternalServerError)
		assertErrorCode(t, rec, "internal_error")

		body := rec.Body.String()
		for _, leak := range []string{"sql", "SELECT", "gorm", ".go:", "/home/", "database"} {
			if strings.Contains(body, leak) {
				t.Errorf("error body should not contain %q, got %s", leak, body)
			}
		}
	}
}
