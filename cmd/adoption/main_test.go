package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/setup"
)

// newDatabase migrates a SQLite database with one linked platform admin.
func newDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "xolo.sqlite")
	t.Setenv("XOLO_STORAGE_DATABASE_DSN", path)
	db, err := setup.OpenOfflineDatabase(path, false)
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	store := adapter.NewStore(db)
	require.NoError(t, store.Migrate(t.Context()))
	tenant, err := store.GetTenantBySlug(t.Context(), model.DefaultTenantSlug)
	require.NoError(t, err)
	admin := model.NewUser(tenant.ID(), "oidc", "root", "root@example.test", "Root", true, model.PlatformRoleUser, model.PlatformRoleAdmin)
	require.NoError(t, store.SaveUser(t.Context(), admin))
	return path
}

func TestArguments(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"import"},
		{"export"},
		{"verify"},
		{"export", "-out", "x", "-in", "y"},
		{"verify", "-in", "x", "-writers-stopped"},
		{"export", "-out", "x", "extra"},
	} {
		require.Error(t, run(t.Context(), args, nil, &bytes.Buffer{}), "%v", args)
	}
}

func TestExportVerifyDetach(t *testing.T) {
	newDatabase(t)
	file := filepath.Join(t.TempDir(), "inventory.ndjson")

	require.NoError(t, run(t.Context(), []string{"export", "-out", file}, nil, &bytes.Buffer{}))
	info, err := os.Stat(file)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.Error(t, run(t.Context(), []string{"export", "-out", file}, nil, &bytes.Buffer{}), "an export never overwrites a file")

	var out bytes.Buffer
	require.NoError(t, run(t.Context(), []string{"verify", "-in", file}, nil, &out))
	var summary struct {
		Count int `json:"count"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &summary))
	require.Equal(t, 2, summary.Count, "the default tenant and its admin")

	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	require.NoError(t, run(t.Context(), []string{"verify", "-in", "-"}, bytes.NewReader(raw), &bytes.Buffer{}))
	require.Error(t, run(t.Context(), []string{"verify", "-in", "-"}, strings.NewReader(string(raw[:len(raw)-2])), &bytes.Buffer{}))

	require.ErrorContains(t, run(t.Context(), []string{"detach", "-writers-stopped"}, nil, &bytes.Buffer{}), "-operator-access-verified")
	out.Reset()
	require.NoError(t, run(t.Context(), []string{"detach", "-writers-stopped", "-operator-access-verified"}, nil, &out))
	require.Contains(t, out.String(), `"subscriptions_removed": 0`)
}

func TestDetachRequiresUsableAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xolo.sqlite")
	t.Setenv("XOLO_STORAGE_DATABASE_DSN", path)
	db, err := setup.OpenOfflineDatabase(path, false)
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, adapter.NewStore(db).Migrate(t.Context()))
	pool.Close()

	err = run(t.Context(), []string{"detach", "-writers-stopped", "-operator-access-verified"}, nil, &bytes.Buffer{})
	require.ErrorIs(t, err, adapter.ErrNoUsableAdmin)
}
