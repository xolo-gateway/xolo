package v1_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
	v1 "github.com/xolo-gateway/xolo/internal/provisionning/handler/v1"
	gormpkg "gorm.io/gorm"
)

func TestFrozenResourceConflict(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	require.NoError(t, err)
	store := xologorm.NewStore(db, xologorm.WithLifecycle(true, time.Hour))
	require.NoError(t, store.PrepareLifecycle(context.Background(), true))
	tenant, err := store.GetTenantBySlug(context.Background(), model.DefaultTenantSlug)
	require.NoError(t, err)
	handler := v1.NewHandler(service.NewProvisioningService(store, store, store, store,
		service.WithProvisioningTransaction(store), service.WithProvisioningReader(store)), testVersion)
	tenantID := string(tenant.ID())
	orgID := putOrganization(t, handler, tenantID, "frozen")

	_, err = store.FreezeResource(context.Background(), model.CommonScope{Family: model.FamilyOrganization, TenantID: tenantID}, orgID, model.MatchCondition{})
	require.NoError(t, err)

	rec := call(t, handler, http.MethodPut, "/v1/tenants/"+tenantID+"/organizations/"+orgID, resource("frozen"))
	assertStatus(t, rec, http.StatusConflict)
	assertErrorCode(t, rec, "resource_deleted")
}
