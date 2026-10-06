package setup

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

func TestConfiguredUserCacheAndInvalidation(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			db, err := gorm.Open(gormlite.Open(filepath.Join(t.TempDir(), "cache.sqlite")), &gorm.Config{})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			raw := adapter.NewStore(db)
			require.NoError(t, raw.Migrate(t.Context()))
			original := getGormStoreFromConfig
			getGormStoreFromConfig = func(context.Context, *config.Config) (*adapter.Store, error) { return raw, nil }
			t.Cleanup(func() { getGormStoreFromConfig = original })
			conf := &config.Config{}
			conf.Storage.Database.Cache.Users = config.StoreCache{Enabled: enabled, Size: 10, TTL: time.Hour}
			store, err := createUserStoreFromConfig(t.Context(), conf)
			require.NoError(t, err)
			tenant, err := raw.GetTenantBySlug(t.Context(), model.DefaultTenantSlug)
			require.NoError(t, err)
			user, err := store.FindOrCreateUser(t.Context(), tenant.ID(), "oidc", "cache")
			require.NoError(t, err)
			queries := 0
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("count_cache_reads", func(*gorm.DB) { queries++ }))
			_, err = store.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", "cache")
			require.NoError(t, err)
			before := queries
			_, err = store.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", "cache")
			require.NoError(t, err)
			if enabled {
				require.Equal(t, before, queries)
			} else {
				require.Greater(t, queries, before)
			}
			copy := model.CopyUser(user)
			copy.SetEmail("Preserved@Example.test")
			require.NoError(t, store.SaveUser(t.Context(), copy))
			stored, err := store.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", "cache")
			require.NoError(t, err)
			require.Equal(t, copy.Email(), stored.Email())
			require.NoError(t, store.DeleteUser(t.Context(), user.ID()))
			_, err = store.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", "cache")
			require.ErrorIs(t, err, port.ErrNotFound)
		})
	}
}
