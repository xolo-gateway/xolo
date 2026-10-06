package cache

import (
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"testing"
	"time"
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
