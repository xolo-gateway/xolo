package v1_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// TestPersonalModelEndpoints reads the personal virtual models of a member,
// and never writes them.
func TestPersonalModelEndpoints(t *testing.T) {
	env := newEnv(t, service.WithMultiTenant(true), service.WithSecretKey(testSecretKey))
	owner := putMember(t, env.handler, env.tenantID, "owner@example.tld")
	other := putMember(t, env.handler, env.tenantID, "other@example.tld")

	vm := model.NewPersonalVirtualModel(model.UserID(owner), "router", "My router")
	require.NoError(t, env.store.CreatePersonalVirtualModel(t.Context(), vm))

	models := env.xoloBase + "/members/" + owner + "/personal-models"
	rec := call(t, env.handler, http.MethodGet, models, nil)
	assertStatus(t, rec, http.StatusOK)
	items, _ := decodeBody(t, rec)["items"].([]any)
	require.Len(t, items, 1)
	require.Equal(t, map[string]any{"tenant_id": env.tenantID, "member_id": owner, "resource_id": string(vm.ID())}, items[0].(map[string]any)["key"])

	rec = call(t, env.handler, http.MethodGet, models+"/"+string(vm.ID()), nil)
	assertStatus(t, rec, http.StatusOK)
	require.NotEmpty(t, rec.Header().Get("ETag"))
	body := decodeBody(t, rec)
	require.Equal(t, "router", body["name"])
	require.Equal(t, "My router", body["description"])
	require.Equal(t, "", body["team_id"])

	// Read-only: no write route.
	assertStatus(t, call(t, env.handler, http.MethodPut, models+"/"+string(vm.ID()), map[string]any{"name": "renamed"}), http.StatusMethodNotAllowed)
	assertStatus(t, call(t, env.handler, http.MethodDelete, models+"/"+string(vm.ID()), nil), http.StatusMethodNotAllowed)

	// A model of another member, of another tenant or of an unknown member is
	// missing.
	assertStatus(t, call(t, env.handler, http.MethodGet, strings.Replace(models, owner, other, 1)+"/"+string(vm.ID()), nil), http.StatusNotFound)
	rec = call(t, env.handler, http.MethodGet, strings.Replace(models, owner, other, 1), nil)
	assertStatus(t, rec, http.StatusOK)
	require.Empty(t, decodeBody(t, rec)["items"])
	otherTenant := uuid.NewString()
	assertStatus(t, call(t, env.handler, http.MethodPut, "/v1/tenants/"+otherTenant, resource("other")), http.StatusOK)
	assertStatus(t, call(t, env.handler, http.MethodGet, strings.Replace(models, env.tenantID, otherTenant, 1)+"/"+string(vm.ID()), nil), http.StatusNotFound)
	assertStatus(t, call(t, env.handler, http.MethodGet, strings.Replace(models, owner, uuid.NewString(), 1), nil), http.StatusNotFound)
	assertStatus(t, call(t, env.handler, http.MethodGet, strings.Replace(models, owner, "not-a-uuid", 1), nil), http.StatusBadRequest)
}
