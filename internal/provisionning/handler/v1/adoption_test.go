package v1_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/adoption"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
	v1 "github.com/xolo-gateway/xolo/internal/provisionning/handler/v1"
	gormpkg "gorm.io/gorm"
)

// newOwnedHandler serves the API over a store enforcing policy, with the
// authority the mTLS middleware sets on every request.
func newOwnedHandler(t *testing.T, policy model.OwnershipPolicy) (http.Handler, string) {
	t.Helper()
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	require.NoError(t, err)
	store := xologorm.NewStore(db, xologorm.WithOwnership(policy))
	tenant, err := store.GetTenantBySlug(context.Background(), model.DefaultTenantSlug)
	require.NoError(t, err)
	handler := v1.NewHandler(service.NewProvisioningService(store, store, store, store,
		service.WithProvisioningTransaction(store), service.WithProvisioningReader(store),
		service.WithInventoryReader(store), service.WithOwnershipPolicy(policy)), testVersion)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(model.WithWriteAuthority(r.Context(), model.OwnerControlPlane)))
	}), string(tenant.ID())
}

func TestOwnershipDenied(t *testing.T) {
	handler, tenantID := newOwnedHandler(t, model.OwnershipPolicy{model.FamilyOrganization: model.OwnerLocal})
	path := "/v1/tenants/" + tenantID + "/organizations/" + uuid.NewString()

	rec := call(t, handler, http.MethodPut, path, resource("local-org"))
	assertStatus(t, rec, http.StatusForbidden)
	require.Equal(t, "ownership_denied", decodeBody(t, rec)["error"].(map[string]any)["code"], rec.Body.String())
	assertStatus(t, call(t, handler, http.MethodGet, path, nil), http.StatusNotFound)

	// Other families stay shared.
	assertStatus(t, call(t, handler, http.MethodPut, "/v1/tenants/"+tenantID+"/members/"+uuid.NewString(), memberBody("someone@example.com")), http.StatusOK)
}

func TestOwnershipEndpoint(t *testing.T) {
	handler, _ := newOwnedHandler(t, model.OwnershipPolicy{model.FamilyMember: model.OwnerControlPlane})
	rec := call(t, handler, http.MethodGet, "/v1/xolo/ownership", nil)
	assertStatus(t, rec, http.StatusOK)
	families := decodeBody(t, rec)["families"].(map[string]any)
	require.Len(t, families, len(model.OwnershipFamilies))
	require.Equal(t, "control_plane", families[model.FamilyMember])
	require.Equal(t, "shared", families[model.FamilyTenant])
	assertStatus(t, call(t, handler, http.MethodGet, "/v1/xolo/ownership?x=1", nil), http.StatusBadRequest)
}

func TestAdoptionExportEndpoint(t *testing.T) {
	handler, tenantID := newOwnedHandler(t, nil)
	assertStatus(t, call(t, handler, http.MethodPut, "/v1/tenants/"+tenantID+"/members/"+uuid.NewString(), memberBody("exported@example.com")), http.StatusOK)

	rec := call(t, handler, http.MethodGet, "/v1/xolo/adoption/export", nil)
	assertStatus(t, rec, http.StatusOK)
	require.Equal(t, "application/x-ndjson", rec.Header().Get("Content-Type"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	summary, err := adoption.Verify(bytes.NewReader(rec.Body.Bytes()))
	require.NoError(t, err)
	require.Equal(t, 1, summary.Families[model.FamilyMember])
	require.Equal(t, 1, summary.Families[model.FamilyTenant])

	// The cursor resumes on the event feed.
	assertStatus(t, call(t, handler, http.MethodGet, "/v1/events?cursor="+summary.Cursor, nil), http.StatusOK)
	assertStatus(t, call(t, handler, http.MethodGet, "/v1/xolo/adoption/export?full=1", nil), http.StatusBadRequest)
}
