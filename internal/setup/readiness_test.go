package setup

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
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
