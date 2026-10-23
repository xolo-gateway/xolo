package v1_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

func virtualModelBody(threshold float64) map[string]any {
	return map[string]any{
		"name": "router", "description": "", "catalog_overrides": nil,
		"graph": map[string]any{
			"nodes": []any{
				map[string]any{"id": "gen", "type": "generator", "position": map[string]any{"x": 0, "y": 0}, "data": map[string]any{}},
				map[string]any{"id": "guard", "type": "plugin", "data": map[string]any{"pluginName": "guard", "config": map[string]any{"threshold": threshold}}},
				map[string]any{"id": "model", "type": "model", "data": map[string]any{"passthrough": true}},
				map[string]any{"id": "sink", "type": "sink", "data": map[string]any{}},
			},
			"edges": []any{
				map[string]any{"id": "e1", "source": "gen", "source_port": "request", "target": "guard", "target_port": "request"},
				map[string]any{"id": "e2", "source": "guard", "source_port": "request", "target": "model", "target_port": "request"},
				map[string]any{"id": "e3", "source": "model", "source_port": "response", "target": "sink", "target_port": "response"},
			},
		},
	}
}

func TestPipelineEndpoints(t *testing.T) {
	env := newEnv(t, service.WithSecretKey(testSecretKey))
	orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", false)
	models := env.xoloBase + "/organizations/" + orgID + "/virtual-models"
	key := uuid.NewString()

	rec := call(t, env.handler, http.MethodPut, models+"/"+key, virtualModelBody(0.5))
	assertStatus(t, rec, http.StatusOK)
	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)

	rec = call(t, env.handler, http.MethodGet, models+"/"+key+"/nodes", nil)
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, etag, rec.Header().Get("ETag"))
	require.Len(t, decodeBody(t, rec)["items"], 4)

	rec = call(t, env.handler, http.MethodGet, models+"/"+key+"/nodes/guard", nil)
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, etag, rec.Header().Get("ETag"))
	node := decodeBody(t, rec)
	require.Equal(t, "plugin", node["type"])
	require.Equal(t, []any{}, node["secret_keys"])

	// A node edit requires the ETag of the virtual model, and moves it.
	update := map[string]any{"data": map[string]any{"pluginName": "guard", "config": map[string]any{"threshold": 0.9}}, "secrets": map[string]any{"token": "s3cr3t-value"}}
	rec = callWithHeaders(t, env.handler, http.MethodPut, models+"/"+key+"/nodes/guard", ifMatchHeader(etag), update)
	assertStatus(t, rec, http.StatusOK)
	next := rec.Header().Get("ETag")
	require.NotEqual(t, etag, next)
	require.NotContains(t, rec.Body.String(), "s3cr3t-value")
	require.Equal(t, []any{"token"}, decodeBody(t, rec)["secret_keys"])

	rec = callWithHeaders(t, env.handler, http.MethodPut, models+"/"+key+"/nodes/guard", ifMatchHeader(etag), update)
	assertStatus(t, rec, http.StatusPreconditionFailed)

	rec = call(t, env.handler, http.MethodGet, models+"/"+key, nil)
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, next, rec.Header().Get("ETag"))
	require.NotContains(t, rec.Body.String(), "s3cr3t-value")
	require.NotContains(t, rec.Body.String(), "fingerprint")

	for name, tc := range map[string]struct {
		path   string
		body   map[string]any
		status int
	}{
		"missing node":      {"/nodes/nope", map[string]any{"data": map[string]any{}}, http.StatusNotFound},
		"missing data":      {"/nodes/guard", map[string]any{"secrets": map[string]any{}}, http.StatusBadRequest},
		"unknown field":     {"/nodes/guard", map[string]any{"data": map[string]any{}, "type": "model"}, http.StatusBadRequest},
		"plugin change":     {"/nodes/guard", map[string]any{"data": map[string]any{"pluginName": "other"}}, http.StatusConflict},
		"secret on builtin": {"/nodes/model", map[string]any{"data": map[string]any{}, "secrets": map[string]any{"k": "v"}}, http.StatusUnprocessableEntity},
	} {
		t.Run(name, func(t *testing.T) {
			assertStatus(t, call(t, env.handler, http.MethodPut, models+"/"+key+tc.path, tc.body), tc.status)
		})
	}

	invalid := virtualModelBody(0.5)
	invalid["graph"].(map[string]any)["edges"] = []any{map[string]any{"id": "e1", "source": "gen", "source_port": "request", "target": "nowhere", "target_port": "request"}}
	rec = call(t, env.handler, http.MethodPut, models+"/"+uuid.NewString(), invalid)
	assertStatus(t, rec, http.StatusUnprocessableEntity)

	middlewares := env.xoloBase + "/organizations/" + orgID + "/middlewares"
	mw := map[string]any{"name": "Guard", "description": "", "enabled": true, "priority": 0, "applies_to_all": true, "targets": []any{},
		"graph": map[string]any{"nodes": []any{}, "edges": []any{}}}
	rec = call(t, env.handler, http.MethodPut, middlewares+"/"+uuid.NewString(), mw)
	assertStatus(t, rec, http.StatusOK)
	rec = call(t, env.handler, http.MethodGet, middlewares, nil)
	assertStatus(t, rec, http.StatusOK)
	require.Len(t, decodeBody(t, rec)["items"], 1)

	rec = call(t, env.handler, http.MethodDelete, models+"/"+key, nil)
	assertStatus(t, rec, http.StatusNoContent)
	rec = call(t, env.handler, http.MethodGet, models+"/"+key+"/nodes", nil)
	assertStatus(t, rec, http.StatusNotFound)
}

// TestPipelineNodesMiddleware serves the nodes of a middleware, refuses a
// change of secret alone to an authority that does not own the family and
// answers 400 to a graph that does not decode.
func TestPipelineNodesMiddleware(t *testing.T) {
	env := newEnv(t, service.WithSecretKey(testSecretKey),
		service.WithOwnershipPolicy(model.OwnershipPolicy{model.FamilyMiddleware: model.OwnerControlPlane}))
	orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", false)
	middlewares := env.xoloBase + "/organizations/" + orgID + "/middlewares"
	key := uuid.NewString()
	body := virtualModelBody(0.5)
	delete(body, "catalog_overrides")
	body["name"], body["enabled"], body["priority"], body["applies_to_all"], body["targets"] = "Guard", true, 0, true, []any{}

	rec := call(t, env.handler, http.MethodPut, middlewares+"/"+key, body)
	assertStatus(t, rec, http.StatusOK)
	rec = call(t, env.handler, http.MethodGet, middlewares+"/"+key+"/nodes/guard", nil)
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, "plugin", decodeBody(t, rec)["type"])

	update := map[string]any{"data": map[string]any{"pluginName": "guard", "config": map[string]any{"threshold": 0.5}}, "secrets": map[string]any{"token": "s3cr3t-value"}}
	rec = call(t, env.handler, http.MethodPut, middlewares+"/"+key+"/nodes/guard", update)
	assertStatus(t, rec, http.StatusForbidden)
	require.Equal(t, "ownership_denied", decodeBody(t, rec)["error"].(map[string]any)["code"], rec.Body.String())

	body["graph"] = "not a graph"
	assertStatus(t, call(t, env.handler, http.MethodPut, middlewares+"/"+uuid.NewString(), body), http.StatusBadRequest)
}
