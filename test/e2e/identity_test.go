//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormadapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/crypto"
)

// TestProvisioningMemberIdentity provisions a member ahead of its first
// sign-in, with a declared identity, then removes it. The account, linked to
// no sign-in, keeps authenticating with its API token.
func TestProvisioningMemberIdentity(t *testing.T) {
	db, err := openDB(env.dsn)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	var tenant gormadapter.Tenant
	require.NoError(t, db.First(&tenant, "slug = ?", model.DefaultTenantSlug).Error)

	memberID := uuid.NewString()
	memberPath := "/v1/tenants/" + tenant.ID + "/members/" + memberID
	identity := map[string]any{"issuer": "https://id.example.test/", "subject": "e2e-declared"}
	body := map[string]any{"email": "e2e-declared@example.tld", "tenant_role": "member", "status": "active", "identity": identity}

	status, raw := provisioningEndpoint.provision(t, http.MethodPut, memberPath, newRequestID(), body)
	require.Equal(t, http.StatusOK, status, string(raw))
	var created map[string]any
	require.NoError(t, json.Unmarshal(raw, &created))
	require.Equal(t, identity, created["identity"])

	status, raw = provisioningEndpoint.provision(t, http.MethodGet, memberPath, newRequestID(), nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	require.Contains(t, string(raw), `"subject":"e2e-declared"`)

	status, raw = provisioningEndpoint.provision(t, http.MethodPut, "/v1/tenants/"+tenant.ID+"/organizations/"+orgAcme+"/members/"+memberID, newRequestID(),
		map[string]any{"role": "member", "status": "active"})
	require.Equal(t, http.StatusOK, status, string(raw))

	const token = "xolo-e2e-declared-member"
	require.NoError(t, db.Create(&gormadapter.AuthToken{
		ID: uuid.NewString(), OwnerID: &memberID, Label: "e2e", Value: crypto.HashToken(token), OrgID: orgAcme,
	}).Error)
	listModels := func() int {
		req, err := http.NewRequest(http.MethodGet, env.baseURL+"/api/v1/models", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	require.Equal(t, http.StatusOK, listModels(), "the token designates its unlinked owner")

	body["identity"] = nil
	status, raw = provisioningEndpoint.provision(t, http.MethodPut, memberPath, newRequestID(), body)
	require.Equal(t, http.StatusOK, status, string(raw))
	require.NotContains(t, string(raw), "identity")
	require.Equal(t, http.StatusOK, listModels())
}
