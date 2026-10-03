package v1_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/adoption"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
	v1 "github.com/xolo-gateway/xolo/internal/provisionning/handler/v1"
)

func TestIdentityExtensionHTTP(t *testing.T) {
	h, db, base := newTestHandler(t)
	uid := string(model.NewUserID())
	path := base + "/members/" + uid
	body := `{"email":"contact@example.test","tenant_role":"member","status":"active","identity":{"issuer":"https://issuer.test/","subject":"Case Sensitive "}}`
	first := call(t, h, "PUT", path, body)
	assertCode(t, first, 200, "")
	for _, identity := range []string{`null`, `{}`, `{"issuer":"https://issuer.test"}`, `{"issuer":"https://issuer.test","subject":""}`, `{"issuer":" https://issuer.test","subject":"one"}`, `{"issuer":"https://issuer.test?","subject":"one"}`, `{"issuer":"https://issuer.test","subject":"one","extra":true}`, `{"issuer":"https://issuer.test","subject":"\ud800"}`, `true`, `[]`} {
		bad := `{"email":"other@test","tenant_role":"member","status":"active","identity":` + identity + `}`
		assertCode(t, call(t, h, "PUT", path, bad), 400, "")
		get := call(t, h, "GET", path, nil)
		require.Equal(t, first.Body.String(), get.Body.String())
		require.Equal(t, first.Header().Get("ETag"), get.Header().Get("ETag"))
	}
	page := decodeResponse[model.CommonPage](t, call(t, h, "GET", base+"/members", nil))
	require.Len(t, page.Items, 1)
	require.JSONEq(t, first.Body.String(), string(page.Items[0].Representation))
	repeated := call(t, h, "PUT", path, body)
	assertCode(t, repeated, 200, "")
	require.Equal(t, first.Header().Get("ETag"), repeated.Header().Get("ETag"))
	assertCode(t, call(t, h, "PUT", base+"/members/"+string(model.NewUserID()), strings.Replace(body, "contact@example.test", "different@example.test", 1)), 409, "conflict")
	exported := call(t, h, "GET", "/v1/xolo/export", nil)
	require.Equal(t, "no-store", exported.Header().Get("Cache-Control"))
	payload, err := adoption.Decode(exported.Body.Bytes())
	require.NoError(t, err)
	require.GreaterOrEqual(t, payload.Count, 2)
	// The manifest remains minimal; ownership and optional features are separate.
	manifest := decodeResponse[map[string]any](t, call(t, h, "GET", "/v1/manifest", nil))
	require.Len(t, manifest, 3)
	s := adapter.NewStore(db)
	require.NoError(t, s.ConfigureOwnership(nil))
	local := v1.NewHandler(service.NewProvisioningService(s, s, s, s))
	assertCode(t, call(t, local, "PUT", path, body), 403, "")
	assertCode(t, call(t, local, "GET", path, nil), 200, "")
	require.NoError(t, s.ConfigureOwnership(model.OwnershipPolicy{"member": "control_plane"}))
	assertCode(t, call(t, local, "PUT", path, body), 200, "")
	discovery := call(t, local, "GET", "/v1/xolo/extensions", nil)
	require.Contains(t, discovery.Body.String(), `"member":"control_plane"`)
	var response map[string]any
	require.NoError(t, json.Unmarshal(discovery.Body.Bytes(), &response))
	require.Len(t, response["extensions"], 3)
}
