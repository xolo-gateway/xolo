package gorm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// TestIdentityMigrationRollback drops the declarations only while there are
// none: removing one would silently detach the member it designates.
func TestIdentityMigrationRollback(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open("file:"+filepath.Join(t.TempDir(), "identity.sqlite")), &gormpkg.Config{})
	require.NoError(t, err)
	store := NewStore(db)
	require.NoError(t, store.Migrate(context.Background()))
	tenant, err := store.GetTenantBySlug(context.Background(), model.DefaultTenantSlug)
	require.NoError(t, err)

	user := model.NewUser(tenant.ID(), "", "", "declared@example.test", "", true, model.PlatformRoleUser)
	user.SetDeclaredIdentity(&model.Identity{Issuer: "https://id.example.test", Subject: "declared"})
	require.NoError(t, store.SaveUser(context.Background(), user))
	require.ErrorContains(t, rollbackIdentity(db), "cannot be rolled back")
	require.True(t, db.Migrator().HasColumn(&User{}, "identity_issuer"))

	user.SetDeclaredIdentity(nil)
	require.NoError(t, store.SaveUser(context.Background(), user))
	require.NoError(t, rollbackIdentity(db))
	require.False(t, db.Migrator().HasColumn(&User{}, "identity_issuer"))
	require.False(t, db.Migrator().HasColumn(&User{}, "identity_subject"))
	require.False(t, db.Migrator().HasIndex(&User{}, declaredIdentityIndex))

	// Applying it again restores the declarations' storage.
	require.NoError(t, migrateIdentity(db))
	require.True(t, db.Migrator().HasIndex(&User{}, declaredIdentityIndex))
}
