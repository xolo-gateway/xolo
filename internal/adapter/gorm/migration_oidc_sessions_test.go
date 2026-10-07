package gorm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/stretchr/testify/require"
	gormpkg "gorm.io/gorm"
)

// TestOIDCSessionsMigrationRollback drops the registry, and applying the
// migration again restores it.
func TestOIDCSessionsMigrationRollback(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open("file:"+filepath.Join(t.TempDir(), "sessions.sqlite")), &gormpkg.Config{})
	require.NoError(t, err)
	require.NoError(t, NewStore(db).Migrate(context.Background()))
	tables := []any{&OIDCIdentity{}, &OIDCSession{}, &OIDCLogoutReplay{}}
	for _, table := range tables {
		require.True(t, db.Migrator().HasTable(table))
	}

	require.NoError(t, rollbackOIDCSessions(db))
	for _, table := range tables {
		require.False(t, db.Migrator().HasTable(table))
	}

	require.NoError(t, migrateOIDCSessions(db))
	for _, table := range tables {
		require.True(t, db.Migrator().HasTable(table))
	}
}
