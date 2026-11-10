package setup

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/config"
)

// On a SQLite file the check pings the reader pool, which stays usable while
// the single writer connection is held by a long write.
func TestReadinessCheckPingsTheReaderOnSQLite(t *testing.T) {
	conf := &config.Config{}
	conf.Storage.AutoMigrate = true
	conf.Storage.Database.DSN = filepath.Join(t.TempDir(), "ready.sqlite")
	conf.Storage.Database.Pool = config.DatabasePool{MaxOpenConns: 4, MaxIdleConns: 1}

	check, err := getReadinessCheckFromConfig(t.Context(), conf)
	require.NoError(t, err)
	require.NoError(t, check(t.Context()))

	writer, err := getGormDatabaseFromConfig(t.Context(), conf)
	require.NoError(t, err)
	pool, err := writer.DB()
	require.NoError(t, err)

	// Hold the only writer connection, as a long write would.
	held, err := pool.Conn(t.Context())
	require.NoError(t, err)
	defer held.Close()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, check(ctx), "readiness must not wait for the writer connection")
}
