package v1_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

func decodeResponse[T any](t *testing.T, r *httptest.ResponseRecorder) T {
	t.Helper()
	require.Equal(t, 200, r.Code, r.Body.String())
	var out T
	require.NoError(t, json.Unmarshal(r.Body.Bytes(), &out))
	return out
}
func conditional(t *testing.T, h http.Handler, path, body string, conditions ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PUT", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range conditions {
		req.Header.Add("If-Match", c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
func TestCommonReadsHTTP(t *testing.T) {
	h, db, base := newTestHandler(t)
	c0 := decodeResponse[map[string]string](t, call(t, h, "GET", "/v1/events/cursor", nil))["cursor"]
	oid, uid := string(model.NewOrgID()), string(model.NewUserID())
	writes := []struct{ path, body, collection string }{
		{base, `{"slug":"default","name":"Tenant","status":"suspended"}`, "/v1/tenants"},
		{base + "/organizations/" + oid, `{"slug":"org","name":"Organization","status":"suspended"}`, base + "/organizations"},
		{base + "/members/" + uid, `{"email":"member@test","tenant_role":"member","status":"suspended"}`, base + "/members"},
		{base + "/domains/a.example.test", `{"status":"suspended"}`, base + "/domains"},
		{base + "/organizations/" + oid + "/members/" + uid, `{"role":"member","status":"suspended"}`, base + "/organizations/" + oid + "/members"},
	}
	for _, w := range writes {
		put := call(t, h, "PUT", w.path, w.body)
		assertCode(t, put, 200, "")
		require.Regexp(t, `^W/"u-[0-9]+"$`, put.Header().Get("ETag"))
		get := call(t, h, "GET", w.path, nil)
		assertCode(t, get, 200, "")
		require.JSONEq(t, put.Body.String(), get.Body.String())
		require.Equal(t, put.Header().Get("ETag"), get.Header().Get("ETag"))
		list := decodeResponse[model.CommonPage](t, call(t, h, "GET", w.collection, nil))
		require.Len(t, list.Items, 1)
		require.Nil(t, list.NextCursor)
		require.JSONEq(t, get.Body.String(), string(list.Items[0].Representation))
		require.Equal(t, get.Header().Get("ETag"), list.Items[0].ETag)
		strong := strings.TrimPrefix(get.Header().Get("ETag"), "W/")
		repeat := conditional(t, h, w.path, w.body, `"unknown"`, strong)
		assertCode(t, repeat, 200, "")
		require.Equal(t, get.Header().Get("ETag"), repeat.Header().Get("ETag"))
		assertCode(t, conditional(t, h, w.path, w.body, `W/"stale"`), 412, "precondition_failed")
		assertCode(t, conditional(t, h, w.path, w.body, `*, "x"`), 400, "invalid_precondition")
		assertCode(t, call(t, h, "GET", w.path+"?limit=2", nil), 400, "invalid_parameter")
	}
	page := decodeResponse[model.CommonEventPage](t, call(t, h, "GET", "/v1/events?cursor="+url.QueryEscape(c0)+"&limit=2", nil))
	require.Len(t, page.Items, 2)
	require.True(t, page.HasMore)
	all := page.Items
	for page.HasMore {
		page = decodeResponse[model.CommonEventPage](t, call(t, h, "GET", "/v1/events?cursor="+url.QueryEscape(page.NextCursor)+"&limit=3", nil))
		all = append(all, page.Items...)
	}
	require.Len(t, all, 5)
	for _, event := range all {
		require.Regexp(t, `^[0-9a-f]{32}$`, event.RequestID)
	}
	for _, q := range []string{"?limit=0", "?limit=1001", "?limit=-1", "?limit=x", "?limit=", "?limit=1&limit=2", "?foo=bar", "?limit=%ZZ"} {
		assertCode(t, call(t, h, "GET", "/v1/tenants"+q, nil), 400, "invalid_parameter")
	}
	for _, q := range []string{"?cursor=", "?cursor=garbage"} {
		assertCode(t, call(t, h, "GET", "/v1/tenants"+q, nil), 400, "invalid_cursor")
	}
	assertCode(t, call(t, h, "GET", "/v1/events", nil), 400, "invalid_parameter")
	assertCode(t, call(t, h, "GET", "/v1/tenants?cursor="+url.QueryEscape(c0), nil), 400, "invalid_cursor")
	assertCode(t, conditional(t, h, "/v1/tenants/"+string(model.NewTenantID()), writes[0].body, "*"), 412, "precondition_failed")
	assertCode(t, call(t, h, "GET", base+"/members/"+string(model.NewUserID()), nil), 404, "not_found")
	assertCode(t, call(t, h, "GET", "/v1/tenants/"+string(model.NewTenantID())+"/members", nil), 404, "parent_not_found")
	require.NoError(t, adapter.NewStore(db).PurgeCommonEvents(t.Context(), time.Now().Add(time.Hour)))
	assertCode(t, call(t, h, "GET", "/v1/events?cursor="+url.QueryEscape(c0), nil), 410, "cursor_expired")
}
