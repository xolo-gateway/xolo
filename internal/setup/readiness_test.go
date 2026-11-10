package setup

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/config"
	"gorm.io/gorm"
)

// On a SQLite file the check pings the reader pool, which stays usable while
// the single writer connection is held by a long write. The handles are the
// test's own: the getters are memoised process-wide.
func TestReadinessCheckPingsTheReaderOnSQLite(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "ready.sqlite")

	writer, err := gorm.Open(gormlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	writerPool, err := writer.DB()
	require.NoError(t, err)
	writerPool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = writerPool.Close() })
	require.NoError(t, writer.Exec("PRAGMA journal_mode=WAL").Error)

	reader, err := adapter.OpenSQLiteReader(dsn, time.Second, &gorm.Config{})
	require.NoError(t, err)
	readerPool, err := reader.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = readerPool.Close() })

	check, err := pingCheck(reader)
	require.NoError(t, err)

	// Hold the only writer connection, as a long write would.
	held, err := writerPool.Conn(t.Context())
	require.NoError(t, err)
	defer held.Close()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, check(ctx), "readiness must not wait for the writer connection")

	// The writer ping, by contrast, waits for the held connection.
	writerCheck, err := pingCheck(writer)
	require.NoError(t, err)
	ctx, cancel = context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	require.Error(t, writerCheck(ctx), "the writer is held: this is what the reader avoids")
}

func TestReadinessDBSelection(t *testing.T) {
	reader, writer := &gorm.DB{}, &gorm.DB{}
	sqlite := &config.Config{}
	sqlite.Storage.Database.DSN = "data.sqlite"

	got, err := readinessDB(sqlite, reader, writer)
	require.NoError(t, err)
	require.Same(t, reader, got, "a SQLite file pings its reader pool")

	got, err = readinessDB(sqlite, nil, writer)
	require.NoError(t, err)
	require.Same(t, writer, got, "an in-memory database, without reader, pings the writer")

	_, err = readinessDB(sqlite, nil, nil)
	require.Error(t, err)
}

// On PostgreSQL the ping has a pool of its own, of one connection, whatever
// the application's pool, and it reports an unreachable server.
func TestReadinessDBOnPostgreSQL(t *testing.T) {
	conf := &config.Config{}
	// Nothing listens on port 1: the probe must fail, and fast.
	conf.Storage.Database.DSN = "postgres://xolo:xolo@127.0.0.1:1/xolo?sslmode=disable&connect_timeout=1"
	conf.Storage.Database.Pool.MaxOpenConns = 40

	db, err := readinessDB(conf, nil, nil)
	require.NoError(t, err, "opening must not connect")
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.Equal(t, 1, pool.Stats().MaxOpenConnections)

	check, err := pingCheck(db)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	require.Error(t, check(ctx))
}
