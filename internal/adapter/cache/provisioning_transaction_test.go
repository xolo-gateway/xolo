package cache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

func TestInvalidationRejectsInFlightRead(t *testing.T) {
	cache := NewMultiIndexCache[*CacheableUser](2, time.Hour)
	user := NewCacheableUser(model.NewUser(model.NewTenantID(), "oidc", "subject", "", "Before", true))
	generation := cache.Generation()
	cache.RemoveMatching(func(*CacheableUser) bool { return true })
	cache.AddIfGeneration(user, generation)
	require.Zero(t, cache.Len(), "a read started before commit must not repopulate the cache")
	cache.AddIfGeneration(user, cache.Generation())
	require.Equal(t, 2, cache.Len())
	cache.RemoveMatching(func(u *CacheableUser) bool { return u.ID() == user.ID() })
	for _, key := range user.CacheKeys() {
		_, ok := cache.Get(key)
		require.False(t, ok)
	}
}

type provisioningBackend struct{ port.ProvisioningTransaction }

func (provisioningBackend) WithProvisioningTransaction(ctx context.Context, fn func(port.ProvisioningTx) error) error {
	return fn(deletingTx{})
}

type deletingTx struct{ port.ProvisioningTx }

func (deletingTx) DeleteProvider(context.Context, model.ProviderID) error { return nil }

// A provider deleted through the provisioning API takes its models with it:
// their cached entries go too.
func TestProvisionedProviderDeletionDropsItsModels(t *testing.T) {
	providers := NewProviderStore(nil, 10, time.Hour)
	m := model.NewLLMModel("prov-1", "org-1", "fast", "fast-real", "", 0, 0)
	providers.modelCache.Add(NewCacheableLLMModel(m))

	transactions := NewProvisioningTransaction(provisioningBackend{}, nil, providers)
	require.NoError(t, transactions.WithProvisioningTransaction(context.Background(), func(tx port.ProvisioningTx) error {
		return tx.DeleteProvider(context.Background(), "prov-1")
	}))

	_, cached := providers.modelCache.Get(getLLMModelProxyNameCacheKey("org-1", "fast"))
	require.False(t, cached, "a model of the deleted provider is still cached")
}
