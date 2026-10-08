package v1_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

const testSecretKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func providerBody(name string) map[string]any {
	return map[string]any{
		"name": name, "type": "openai", "base_url": "https://llm.example.test/v1", "active": true, "currency": "EUR",
		"cloud_tier": 0, "billing_mode": "payg", "subscription_plan": nil, "retry_config": nil, "rate_limit_config": nil,
		"api_key": "sk-provisioned-secret",
	}
}

func TestBusinessEndpoints(t *testing.T) {
	env := newEnv(t, service.WithSecretKey(testSecretKey))
	orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", false)
	providers := env.xoloBase + "/organizations/" + orgID + "/providers"
	key := uuid.NewString()

	rec := call(t, env.handler, http.MethodPut, providers+"/"+key, providerBody("OpenAI"))
	assertStatus(t, rec, http.StatusOK)
	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)
	require.NotContains(t, rec.Body.String(), "sk-provisioned-secret")
	require.NotContains(t, rec.Body.String(), "api_key")

	rec = call(t, env.handler, http.MethodGet, providers+"/"+key, nil)
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, etag, rec.Header().Get("ETag"))
	require.Equal(t, "OpenAI", decodeBody(t, rec)["name"])

	rec = call(t, env.handler, http.MethodGet, providers, nil)
	assertStatus(t, rec, http.StatusOK)
	require.Len(t, decodeBody(t, rec)["items"], 1)
	require.NotContains(t, rec.Body.String(), "sk-provisioned-secret")

	// The key is write-only: omitting it keeps it.
	body := providerBody("OpenAI")
	delete(body, "api_key")
	rec = callWithHeaders(t, env.handler, http.MethodPut, providers+"/"+key, ifMatchHeader(etag), body)
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, etag, rec.Header().Get("ETag"))

	for name, mutate := range map[string]func(map[string]any){
		"missing field":      func(b map[string]any) { delete(b, "retry_config") },
		"null name":          func(b map[string]any) { b["name"] = nil },
		"unknown field":      func(b map[string]any) { b["secret"] = "x" },
		"wrong type":         func(b map[string]any) { b["active"] = "yes" },
		"null api_key":       func(b map[string]any) { b["api_key"] = nil },
		"unknown type":       func(b map[string]any) { b["type"] = "unknown" },
		"credentials url":    func(b map[string]any) { b["base_url"] = "https://user:pass@llm.example.test" },
		"unknown currency":   func(b map[string]any) { b["currency"] = "XXX" },
		"unknown billing":    func(b map[string]any) { b["billing_mode"] = "free" },
		"invalid cloud tier": func(b map[string]any) { b["cloud_tier"] = 7 },
	} {
		body := providerBody("OpenAI")
		mutate(body)
		rec := call(t, env.handler, http.MethodPut, providers+"/"+uuid.NewString(), body)
		require.Contains(t, []int{http.StatusBadRequest, http.StatusUnprocessableEntity}, rec.Code, name+": "+rec.Body.String())
	}

	// Quotas hang from the tenant.
	quotas := env.xoloBase + "/quotas"
	rec = call(t, env.handler, http.MethodPut, quotas+"/"+uuid.NewString(), map[string]any{
		"scope": "org", "scope_id": orgID, "currency": "EUR", "daily_budget": nil, "monthly_budget": 1000, "yearly_budget": nil,
	})
	assertStatus(t, rec, http.StatusOK)
	rec = call(t, env.handler, http.MethodPut, quotas+"/"+uuid.NewString(), map[string]any{
		"scope": "org", "scope_id": uuid.NewString(), "currency": "EUR", "daily_budget": nil, "monthly_budget": nil, "yearly_budget": nil,
	})
	assertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, "parent_not_found")

	rec = call(t, env.handler, http.MethodGet, strings.Replace(providers, orgID, uuid.NewString(), 1), nil)
	assertStatus(t, rec, http.StatusNotFound)
}
