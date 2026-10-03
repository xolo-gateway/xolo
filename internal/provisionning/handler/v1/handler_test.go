package v1_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
	v1 "github.com/xolo-gateway/xolo/internal/provisionning/handler/v1"
	"gorm.io/gorm"
)

func newTestHandler(t *testing.T) (*v1.Handler, *gorm.DB, string) {
	db, err := gorm.Open(gormlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { pool.Close() })
	s := adapter.NewStore(db)
	tenant, err := s.GetTenantBySlug(t.Context(), "default")
	require.NoError(t, err)
	return v1.NewHandler(service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true), service.WithReservedEmails("root@example.test")), "test-release"), db, "/v1/tenants/" + string(tenant.ID())
}
func call(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if s, ok := body.(string); ok {
		raw = []byte(s)
	} else if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req = req.WithContext(model.WithActor(req.Context(), model.Actor{URI: "urn:test:console", RequestID: strings.Repeat("a", 32)}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
func assertCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	require.Equal(t, status, rec.Code, rec.Body.String())
	if code != "" {
		require.Contains(t, rec.Body.String(), `"code":"`+code+`"`)
	}
}

func TestManifestAndRemovedRoutes(t *testing.T) {
	h, _, base := newTestHandler(t)
	rec := call(t, h, "GET", "/v1/manifest", nil)
	assertCode(t, rec, 200, "")
	require.JSONEq(t, `{"name":"Xolo","version":"test-release","contract_version":"0.1.0-draft.1"}`, rec.Body.String())
	for _, route := range [][2]string{{"POST", "/v1/tenants"}, {"PATCH", base}, {"DELETE", base}, {"GET", base + "/users"}, {"PUT", base + "/users"}, {"POST", base + "/organizations"}, {"POST", "/v1/manifest"}, {"HEAD", "/v1/manifest"}, {"GET", "/v2/manifest"}} {
		assertCode(t, call(t, h, route[0], route[1], nil), 404, "not_found")
	}
	assertCode(t, call(t, h, "GET", "/v1/xolo/permissions", nil), 200, "")
}
func TestCommonPUTsAndNoOps(t *testing.T) {
	h, db, base := newTestHandler(t)
	org := base + "/organizations/" + string(model.NewOrgID())
	member := base + "/members/" + string(model.NewUserID())
	uid := member[strings.LastIndex(member, "/")+1:]
	writes := []struct{ path, body, expected string }{
		{base, `{"slug":" DEFAULT ","name":"  My tenant  ","status":" active "}`, `{"slug":"default","name":"My tenant","status":"active"}`},
		{org, `{"slug":" SALES ","name":" Équipe ","status":"active"}`, `{"slug":"sales","name":"Équipe","status":"active"}`},
		{member, `{"email":" PERSON@EXAMPLE.TEST ","display_name":" Alice ","tenant_role":" owner ","status":"active"}`, `{"email":"person@example.test","display_name":"Alice","tenant_role":"owner","status":"active"}`},
		{org + "/members/" + uid, `{"role":" owner ","status":" active "}`, `{"role":"owner","status":"active"}`},
		{base + "/domains/Customer.Example.test", `{"status":" active "}`, `{"status":"active"}`},
	}
	for _, write := range writes {
		rec := call(t, h, "PUT", write.path, write.body)
		assertCode(t, rec, 200, "")
		require.JSONEq(t, write.expected, rec.Body.String())
		var before, after []adapter.MutationAudit
		require.NoError(t, db.Order("sequence").Find(&before).Error)
		var timesBefore, timesAfter []adapter.User
		require.NoError(t, db.Find(&timesBefore).Error)
		rec = call(t, h, "PUT", write.path, write.expected)
		assertCode(t, rec, 200, "")
		require.NoError(t, db.Order("sequence").Find(&after).Error)
		require.Equal(t, before, after)
		require.NoError(t, db.Find(&timesAfter).Error)
		require.Equal(t, timesBefore, timesAfter)
	}
	var user adapter.User
	require.NoError(t, db.First(&user, "id = ?", uid).Error)
	require.Empty(t, user.Provider)
	require.Empty(t, user.Subject)
	require.Empty(t, user.Roles)
	var audit adapter.MutationAudit
	require.NoError(t, db.First(&audit).Error)
	require.Equal(t, strings.Repeat("a", 32), audit.RequestID)
	// Omission clears the optional field; suspension of a parent remains legal.
	assertCode(t, call(t, h, "PUT", member, `{"email":"person@example.test","tenant_role":"owner","status":"active"}`), 200, "")
	require.NoError(t, db.First(&user, "id = ?", uid).Error)
	require.Empty(t, user.DisplayName)
	assertCode(t, call(t, h, "PUT", member, `{"email":"person@example.test","tenant_role":"member","status":"active"}`), 409, "last_owner")
	assertCode(t, call(t, h, "PUT", org+"/members/"+uid, `{"role":"owner","status":"suspended"}`), 409, "last_owner")
	assertCode(t, call(t, h, "PUT", base, `{"slug":"default","name":"My tenant","status":"suspended"}`), 200, "")
	assertCode(t, call(t, h, "PUT", org, `{"slug":"renamed","name":"Still managed","status":"suspended"}`), 200, "")
}
func TestStrictRepresentations(t *testing.T) {
	h, _, base := newTestHandler(t)
	path := base + "/members/" + string(model.NewUserID())
	valid := `{"email":"a@b","tenant_role":"member","status":"active"}`
	for _, tc := range []struct{ body, code string }{
		{valid + " {}", "invalid_json"}, {"[]", "invalid_json"}, {"null", "invalid_representation"}, {"{}", "invalid_representation"},
		{`{"email":null,"tenant_role":"member","status":"active"}`, "invalid_representation"},
		{`{"email":12,"tenant_role":"member","status":"active"}`, "invalid_json"},
		{`{"email":"a@b","tenant_role":"member","status":" active "}`, "invalid_representation"},
		{`{"email":"a@b","tenant_role":"Member","status":"active"}`, "invalid_representation"},
		{strings.Replace(valid, `"a@b"`, `"a@b","roles":["admin"]`, 1), "invalid_json"},
		{strings.Replace(valid, "a@b", strings.Repeat("é", 160)+"@", 1), "invalid_representation"},
		{strings.Replace(valid, "a@b", `\ud800@b`, 1), "invalid_json"},
		{strings.Replace(valid, "a@b", string([]byte{0xff})+"@b", 1), "invalid_json"},
		{valid + strings.Repeat(" ", (1<<20)-len(valid)+1), "invalid_json"},
	} {
		assertCode(t, call(t, h, "PUT", path, tc.body), 400, tc.code)
	}
	assertCode(t, call(t, h, "PUT", path, valid+strings.Repeat(" ", (1<<20)-len(valid))), 200, "")
	for _, ct := range []string{"", "text/plain", "application/json; nonsense"} {
		req := httptest.NewRequest("PUT", path, strings.NewReader(valid))
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assertCode(t, rec, 415, "unsupported_media_type")
	}
	assertCode(t, call(t, h, "PUT", base+"/members/UPPERCASE", valid), 400, "invalid_representation")
	assertCode(t, call(t, h, "PUT", base+"/domains/127.0.0.1", `{"status":"active"}`), 400, "invalid_hostname")
	assertCode(t, call(t, h, "PUT", path, strings.Replace(valid, "a@b", "root@example.test", 1)), 409, "conflict")
}
func TestScopeConflicts(t *testing.T) {
	h, db, base := newTestHandler(t)
	other := "/v1/tenants/" + string(model.NewTenantID())
	resource := `{"slug":"other","name":"Other","status":"active"}`
	assertCode(t, call(t, h, "PUT", other, resource), 200, "")
	oid := string(model.NewOrgID())
	uid := string(model.NewUserID())
	member := `{"email":"a@b","tenant_role":"member","status":"active"}`
	assertCode(t, call(t, h, "PUT", base+"/organizations/"+oid, resource), 200, "")
	assertCode(t, call(t, h, "PUT", base+"/members/"+uid, member), 200, "")
	for _, route := range []string{other + "/organizations/" + oid, other + "/members/" + uid} {
		body := resource
		if strings.Contains(route, "/members/") {
			body = member
		}
		rec := call(t, h, "PUT", route, body)
		assertCode(t, rec, 409, "conflict")
		require.NotContains(t, rec.Body.String(), base)
	}
	assertCode(t, call(t, h, "PUT", other+"/organizations/"+oid+"/members/"+uid, `{"role":"member","status":"active"}`), 404, "parent_not_found")
	assertCode(t, call(t, h, "PUT", base+"/domains/example.test", `{"status":"active"}`), 200, "")
	assertCode(t, call(t, h, "PUT", other+"/domains/example.test", `{"status":"active"}`), 409, "conflict")
	require.NoError(t, adapter.NewStore(db).ReserveDomain(t.Context(), "shared.example.test"))
	assertCode(t, call(t, h, "PUT", base+"/domains/shared.example.test", `{"status":"active"}`), 400, "invalid_hostname")
	assertCode(t, call(t, h, "PUT", base, resource), 409, "conflict")
}

func TestXoloExtensionsAndCustomRoles(t *testing.T) {
	h, db, base := newTestHandler(t)
	oid := string(model.NewOrgID())
	uid := string(model.NewUserID())
	org := base + "/organizations/" + oid
	assertCode(t, call(t, h, "PUT", org, `{"slug":"org","name":"Org","status":"active"}`), 200, "")
	assertCode(t, call(t, h, "PUT", base+"/members/"+uid, `{"email":"user@example.test","tenant_role":"member","status":"active"}`), 200, "")
	assertCode(t, call(t, h, "PUT", org+"/members/"+uid, `{"role":"member","status":"active"}`), 200, "")
	ext := strings.Replace(org, "/v1/", "/v1/xolo/", 1)
	rec := call(t, h, "POST", ext+"/roles", `{"name":"Custom","permissions":[]}`)
	assertCode(t, rec, 201, "")
	var role struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &role))
	require.NotEmpty(t, role.ID)
	var membership adapter.Membership
	require.NoError(t, db.First(&membership, "user_id = ? AND org_id = ?", uid, oid).Error)
	assertCode(t, call(t, h, "PUT", ext+"/members/"+membership.ID+"/roles", map[string]any{"roleIds": []string{role.ID}, "builtinRoles": []string{"member"}}), 200, "")
	assertCode(t, call(t, h, "PUT", org+"/members/"+uid, `{"role":"admin","status":"active"}`), 200, "")
	var assignments int64
	require.NoError(t, db.Model(&adapter.MembershipRole{}).Where("membership_id = ? AND role_id = ?", membership.ID, role.ID).Count(&assignments).Error)
	require.EqualValues(t, 1, assignments)
	assertCode(t, call(t, h, "PATCH", ext, `{"currency":"USD","shareQuotaEqually":true}`), 200, "")
	rec = call(t, h, "GET", ext, nil)
	assertCode(t, rec, 200, "")
	require.Contains(t, rec.Body.String(), `"currency":"USD"`)
	assertCode(t, call(t, h, "PUT", strings.Replace(base, "/v1/", "/v1/xolo/", 1)+"/users", `{"provider":"oidc","subject":"external","email":"identity@example.test"}`), 201, "")
}
