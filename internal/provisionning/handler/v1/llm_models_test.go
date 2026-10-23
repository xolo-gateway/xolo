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

// TestLLMModelEndpoints reads the models of an organization, disabled ones
// included, and never writes them.
func TestLLMModelEndpoints(t *testing.T) {
	env := newEnv(t, service.WithMultiTenant(true), service.WithSecretKey(testSecretKey))
	orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", false)
	providerID := uuid.NewString()
	assertStatus(t, call(t, env.handler, http.MethodPut, env.xoloBase+"/organizations/"+orgID+"/providers/"+providerID, providerBody("OpenAI")), http.StatusOK)

	enabled := model.NewLLMModel(model.ProviderID(providerID), model.OrgID(orgID), "gpt-4o", "gpt-4o-2024-08-06", "", 0, 0)
	require.NoError(t, env.store.CreateLLMModel(t.Context(), enabled))
	disabled := model.NewLLMModel(model.ProviderID(providerID), model.OrgID(orgID), "meta/llama", "llama", "", 0, 0)
	disabled.SetEnabled(false)
	require.NoError(t, env.store.CreateLLMModel(t.Context(), disabled))

	// Virtual models have their own family.
	assertStatus(t, call(t, env.handler, http.MethodPut, env.xoloBase+"/organizations/"+orgID+"/virtual-models/"+uuid.NewString(), virtualModelBody(0.5)), http.StatusOK)

	models := env.xoloBase + "/organizations/" + orgID + "/llm-models"
	rec := call(t, env.handler, http.MethodGet, models, nil)
	assertStatus(t, rec, http.StatusOK)
	require.Len(t, decodeBody(t, rec)["items"], 2)
	require.NotContains(t, rec.Body.String(), "gpt-4o-2024-08-06", "the upstream model is not exposed")

	rec = call(t, env.handler, http.MethodGet, models+"/"+string(disabled.ID()), nil)
	assertStatus(t, rec, http.StatusOK)
	require.NotEmpty(t, rec.Header().Get("ETag"))
	require.Equal(t, map[string]any{"provider_id": providerID, "proxy_name": "meta/llama", "kind": "completion", "enabled": false}, decodeBody(t, rec))

	// Read-only: no write route.
	rec = call(t, env.handler, http.MethodPut, models+"/"+string(enabled.ID()), map[string]any{"provider_id": providerID, "proxy_name": "renamed", "kind": "completion", "enabled": true})
	assertStatus(t, rec, http.StatusMethodNotAllowed)
	rec = call(t, env.handler, http.MethodDelete, models+"/"+string(enabled.ID()), nil)
	assertStatus(t, rec, http.StatusMethodNotAllowed)
	_, err := env.store.GetLLMModelByID(t.Context(), enabled.ID())
	require.NoError(t, err)

	// A model of another organization is missing.
	otherID, _ := createOrganization(t, env.handler, env.tenantID, "globex", false)
	rec = call(t, env.handler, http.MethodGet, strings.Replace(models, orgID, otherID, 1)+"/"+string(enabled.ID()), nil)
	assertStatus(t, rec, http.StatusNotFound)
	rec = call(t, env.handler, http.MethodGet, strings.Replace(models, orgID, otherID, 1), nil)
	assertStatus(t, rec, http.StatusOK)
	require.Empty(t, decodeBody(t, rec)["items"])

	// Under another tenant, or an unknown organization, too.
	otherTenant := uuid.NewString()
	assertStatus(t, call(t, env.handler, http.MethodPut, "/v1/tenants/"+otherTenant, resource("other")), http.StatusOK)
	rec = call(t, env.handler, http.MethodGet, strings.Replace(models, env.tenantID, otherTenant, 1)+"/"+string(enabled.ID()), nil)
	assertStatus(t, rec, http.StatusNotFound)
	rec = call(t, env.handler, http.MethodGet, strings.Replace(models, orgID, uuid.NewString(), 1), nil)
	assertStatus(t, rec, http.StatusNotFound)
	rec = call(t, env.handler, http.MethodGet, strings.Replace(models, orgID, uuid.NewString(), 1)+"/"+string(enabled.ID()), nil)
	assertStatus(t, rec, http.StatusNotFound)
}
