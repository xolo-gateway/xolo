package v1_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
	v1 "github.com/xolo-gateway/xolo/internal/provisionning/handler/v1"
)

func TestLifecycleHTTPProfile(t *testing.T) {
	h, db, base := newTestHandler(t)
	s := adapter.NewStore(db)
	require.NoError(t, s.Migrate(t.Context()))
	h.WithLifecycle(service.NewLifecycleService(s, time.Hour, time.Second))
	require.Contains(t, call(t, h, "GET", "/v1/xolo/extensions", nil).Body.String(), "lifecycle")
	require.NotContains(t, call(t, h, "GET", "/v1/manifest", nil).Body.String(), "lifecycle")
	for _, body := range []string{"{}", "null", " "} {
		assertCode(t, call(t, h, "DELETE", base, body), 400, "invalid_representation")
	}
	assertCode(t, call(t, h, "DELETE", base+"?unexpected=1", nil), 400, "invalid_parameter")
	deleted := call(t, h, "DELETE", base, nil)
	assertCode(t, deleted, 202, "")
	etag := deleted.Header().Get("ETag")
	require.Equal(t, etag, call(t, h, "DELETE", base, nil).Header().Get("ETag"))
	assertCode(t, conditional(t, h, base, `{"slug":"default","name":"revive","status":"active"}`), 410, "resource_deleted")
	require.Contains(t, call(t, h, "GET", base, nil).Body.String(), `"deleted"`)
	export := call(t, h, "GET", base+"/deletion/export", nil)
	assertCode(t, export, 200, "")
	require.Equal(t, "no-store", export.Header().Get("Cache-Control"))
	var archive struct {
		Digest string `json:"sha256"`
	}
	require.NoError(t, json.Unmarshal(export.Body.Bytes(), &archive))
	confirm := func(tag, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", base+"/purge-confirmation", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if tag != "" {
			r.Header.Set("If-Match", tag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	body := `{"export_sha256":"` + archive.Digest + `"}`
	assertCode(t, confirm("", body), 428, "precondition_required")
	assertCode(t, confirm("*", body), 428, "precondition_required")
	assertCode(t, confirm(`W/"stale"`, body), 412, "precondition_failed")
	for _, bad := range []string{"null", body + body, `{"export_sha256":null}`, `{"export_sha256":"` + archive.Digest + `","extra":true}`} {
		require.Equal(t, 400, confirm(etag, bad).Code)
	}
	assertCode(t, confirm(etag, body), 200, "")
	assertCode(t, confirm(etag, body), 200, "")
	require.Equal(t, etag, call(t, h, "GET", base+"/deletion", nil).Header().Get("ETag"))
}
func TestBusinessHTTPStrictRepresentationsAndDiscovery(t *testing.T) {
	_, db, base := newTestHandler(t)
	s := adapter.NewStore(db)
	require.NoError(t, s.Migrate(t.Context()))
	s.ConfigureBusiness(true, strings.Repeat("11", 32))
	h := v1.NewHandler(service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))).WithBusiness()
	tid := strings.TrimPrefix(base, "/v1/tenants/")
	oid := string(model.NewOrgID())
	assertCode(t, call(t, h, "PUT", base+"/organizations/"+oid, `{"slug":"business","name":"Business","status":"active"}`), 200, "")
	path := "/v1/xolo/tenants/" + tid + "/organizations/" + oid + "/applications/" + string(model.NewApplicationID())
	body := `{"name":"App","description":"","active":true,"role_ids":[]}`
	put := conditional(t, h, path, body)
	assertCode(t, put, 200, "")
	require.JSONEq(t, body, put.Body.String())
	for _, bad := range []string{`{"name":"App"}`, `{"name":"App","description":"","active":null,"role_ids":[]}`, body + body, strings.Replace(body, `"role_ids":[]`, `"role_ids":[],"secret":"hidden"`, 1)} {
		assertCode(t, conditional(t, h, path, bad), 400, "invalid_representation")
	}
	assertCode(t, conditional(t, h, path, body, `W/"stale"`), 412, "precondition_failed")
	get := call(t, h, "GET", path, nil)
	require.Equal(t, put.Header().Get("ETag"), get.Header().Get("ETag"))
	list := call(t, h, "GET", path[:strings.LastIndex(path, "/")]+"?limit=1", nil)
	assertCode(t, list, 200, "")
	require.Contains(t, list.Body.String(), "resource_id")
	require.Contains(t, call(t, h, "GET", "/v1/xolo/extensions", nil).Body.String(), "business_resources")
}
