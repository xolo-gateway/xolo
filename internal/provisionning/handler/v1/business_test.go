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

	// The headers a gateway requires are optional, and read back.
	body = providerBody("OpenCode Go")
	body["request_headers"] = map[string]any{"headers": map[string]any{"User-Agent": "xolo/1.0"}, "session_header": "x-opencode-session"}
	rec = call(t, env.handler, http.MethodPut, providers+"/"+key, body)
	assertStatus(t, rec, http.StatusOK)
	rec = call(t, env.handler, http.MethodGet, providers+"/"+key, nil)
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, map[string]any{"headers": map[string]any{"User-Agent": "xolo/1.0"}, "session_header": "x-opencode-session"},
		decodeBody(t, rec)["request_headers"])
	body["request_headers"] = nil
	rec = call(t, env.handler, http.MethodPut, providers+"/"+key, body)
	assertStatus(t, rec, http.StatusOK)
	rec = call(t, env.handler, http.MethodGet, providers+"/"+key, nil)
	assertStatus(t, rec, http.StatusOK)
	require.Nil(t, decodeBody(t, rec)["request_headers"], "an explicit null clears the headers")

	for name, mutate := range map[string]func(map[string]any){
		"reserved header": func(b map[string]any) {
			b["request_headers"] = map[string]any{"headers": map[string]any{"Authorization": "Bearer x"}}
		},
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

func TestTeamEndpoints(t *testing.T) {
	env := newEnv(t, service.WithSecretKey(testSecretKey))
	orgID, _ := createOrganization(t, env.handler, env.tenantID, "acme", false)
	userID, _ := addOrgMember(t, env.handler, env.tenantID, orgID, "member@example.tld", "member")
	teams := env.tenantBase + "/organizations/" + orgID + "/teams"
	key := uuid.NewString()
	body := map[string]any{"slug": " LEGAL ", "name": "Juridique", "status": "active"}

	// The team belongs to the common contract.
	rec := call(t, env.handler, http.MethodPut, teams+"/"+key, body)
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, map[string]any{"slug": "legal", "name": "Juridique", "status": "active"}, decodeBody(t, rec))
	etag := rec.Header().Get("ETag")
	rec = call(t, env.handler, http.MethodPut, teams+"/"+key, body)
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, etag, rec.Header().Get("ETag"), "an identical PUT changes nothing")

	rec = call(t, env.handler, http.MethodGet, teams, nil)
	assertStatus(t, rec, http.StatusOK)
	items := decodeBody(t, rec)["items"].([]any)
	require.Len(t, items, 1)
	require.Equal(t, map[string]any{"tenant_id": env.tenantID, "organization_id": orgID, "team_id": key}, items[0].(map[string]any)["key"])

	body["slug"] = "Not a slug"
	rec = call(t, env.handler, http.MethodPut, teams+"/"+key, body)
	require.Contains(t, []int{http.StatusBadRequest, http.StatusUnprocessableEntity}, rec.Code, rec.Body.String())
	body["slug"] = "legal"
	rec = call(t, env.handler, http.MethodPut, teams+"/"+key, map[string]any{"slug": "legal", "name": "Juridique", "status": "active", "members": []any{}})
	assertStatus(t, rec, http.StatusBadRequest)
	rec = call(t, env.handler, http.MethodPut, teams+"/"+strings.ToUpper(key), body)
	assertStatus(t, rec, http.StatusBadRequest)

	// A team key never moves to another organization.
	otherOrg, _ := createOrganization(t, env.handler, env.tenantID, "other", false)
	rec = call(t, env.handler, http.MethodPut, env.tenantBase+"/organizations/"+otherOrg+"/teams/"+key, body)
	assertStatus(t, rec, http.StatusConflict)
	assertErrorCode(t, rec, "conflict")

	// Team memberships need the organization membership and the team.
	members := teams + "/" + key + "/members"
	rec = call(t, env.handler, http.MethodPut, members+"/"+userID, map[string]any{"status": "active"})
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, map[string]any{"status": "active"}, decodeBody(t, rec))
	rec = call(t, env.handler, http.MethodPut, members+"/"+userID, map[string]any{"status": "active", "role": "member"})
	assertStatus(t, rec, http.StatusBadRequest)
	stranger := putMember(t, env.handler, env.tenantID, "stranger@example.tld")
	rec = call(t, env.handler, http.MethodPut, members+"/"+stranger, map[string]any{"status": "active"})
	assertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, "parent_not_found")
	rec = call(t, env.handler, http.MethodPut, teams+"/"+uuid.NewString()+"/members/"+userID, map[string]any{"status": "active"})
	assertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, "parent_not_found")
	rec = call(t, env.handler, http.MethodGet, members, nil)
	assertStatus(t, rec, http.StatusOK)
	items = decodeBody(t, rec)["items"].([]any)
	require.Len(t, items, 1)
	require.Equal(t, map[string]any{"tenant_id": env.tenantID, "organization_id": orgID, "team_id": key, "member_id": userID}, items[0].(map[string]any)["key"])
	rec = call(t, env.handler, http.MethodGet, members+"/"+userID, nil)
	assertStatus(t, rec, http.StatusOK)

	// The settings live under /v1/xolo and exist with their team only.
	settings := env.xoloBase + "/organizations/" + orgID + "/team-settings"
	settingsBody := map[string]any{"description": "Contrats", "share_quota_equally": true, "model_grants": []any{}, "middleware_ids": []any{}, "member_roles": []any{}}
	rec = call(t, env.handler, http.MethodPut, settings+"/"+key, settingsBody)
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, "Contrats", decodeBody(t, rec)["description"])
	rec = call(t, env.handler, http.MethodPut, settings+"/"+uuid.NewString(), settingsBody)
	assertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, "parent_not_found")
	delete(settingsBody, "member_roles")
	rec = call(t, env.handler, http.MethodPut, settings+"/"+key, settingsBody)
	assertStatus(t, rec, http.StatusBadRequest)
	rec = call(t, env.handler, http.MethodDelete, settings+"/"+key, nil)
	assertStatus(t, rec, http.StatusMethodNotAllowed)
	rec = call(t, env.handler, http.MethodGet, settings, nil)
	assertStatus(t, rec, http.StatusOK)
	require.Len(t, decodeBody(t, rec)["items"], 1)

	// A team budget hangs from the tenant, like every quota.
	rec = call(t, env.handler, http.MethodPut, env.xoloBase+"/quotas/"+uuid.NewString(), map[string]any{
		"scope": "team", "scope_id": key, "currency": "EUR", "daily_budget": nil, "monthly_budget": 1000, "yearly_budget": nil, "cascade": "split",
	})
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, "split", decodeBody(t, rec)["cascade"])

	// Deletions are immediate and idempotent.
	rec = call(t, env.handler, http.MethodDelete, members+"/"+userID, nil)
	assertStatus(t, rec, http.StatusNoContent)
	rec = call(t, env.handler, http.MethodDelete, members+"/"+userID, nil)
	assertStatus(t, rec, http.StatusNoContent)
	rec = call(t, env.handler, http.MethodDelete, teams+"/"+key, nil)
	assertStatus(t, rec, http.StatusNoContent)
	rec = call(t, env.handler, http.MethodGet, teams+"/"+key, nil)
	assertStatus(t, rec, http.StatusNotFound)
	rec = call(t, env.handler, http.MethodGet, settings+"/"+key, nil)
	assertStatus(t, rec, http.StatusNotFound)
}
