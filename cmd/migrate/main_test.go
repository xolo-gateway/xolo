package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
)

func TestOfflineMigrationWorkflow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	t.Setenv("XOLO_STORAGE_DATABASE_DSN", path)
	db, err := openDatabase(path, false)
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	require.NoError(t, adapter.NewStore(db).Migrate(t.Context()))
	require.NoError(t, db.Create(&adapter.Tenant{ID: "tenant-acme", Slug: "acme", Name: "Acme", Active: 1}).Error)
	require.NoError(t, db.Create(&adapter.User{ID: "user-one", TenantID: "tenant-acme", Email: "One@example.test"}).Error)
	require.NoError(t, db.Create(&adapter.User{ID: "user-two", TenantID: "tenant-acme", Email: "one@example.test"}).Error)
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610020001").Error)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	var out bytes.Buffer
	require.ErrorContains(t, run(t.Context(), []string{"diagnose"}, &out), "migration blocked")
	require.Contains(t, out.String(), "normalized email collision")
	planPath := filepath.Join(t.TempDir(), "recovery.json")
	require.NoError(t, run(t.Context(), []string{"plan", "-out", planPath}, &out))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "diagnose and plan must not change the database")
	info, err := os.Stat(planPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.Error(t, run(t.Context(), []string{"plan", "-out", planPath}, &out), "must not overwrite a saved mapping")
	raw, err := os.ReadFile(planPath)
	require.NoError(t, err)
	var artifact adapter.RecoveryArtifact
	require.NoError(t, json.Unmarshal(raw, &artifact))
	artifact.EmailOverrides = map[string]string{"user-two": "two@example.test"}
	raw, err = json.Marshal(artifact)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(planPath, raw, 0600))
	require.NoError(t, run(t.Context(), []string{"diagnose", "-plan", planPath}, &out))
	require.ErrorContains(t, run(t.Context(), []string{"apply", "-plan", planPath}, &out), "writers-stopped")
	require.NoError(t, run(t.Context(), []string{"apply", "-plan", planPath, "-writers-stopped"}, &out))
	require.NoError(t, run(t.Context(), []string{"apply", "-plan", planPath, "-writers-stopped"}, &out))
	require.NoError(t, run(t.Context(), []string{"diagnose"}, &out))
	var user adapter.User
	require.NoError(t, db.First(&user, "id = ?", artifact.IDs["users"]["user-two"]).Error)
	require.Equal(t, "two@example.test", user.Email)
	require.NoError(t, adapter.CheckDatabaseSchema(t.Context(), db))
}

func TestReadOnlyCommandsDoNotCreateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	t.Setenv("XOLO_STORAGE_DATABASE_DSN", path)
	for _, action := range []string{"diagnose", "plan"} {
		args := []string{action}
		if action == "plan" {
			args = append(args, "-out", filepath.Join(t.TempDir(), "plan.json"))
		}
		var out bytes.Buffer
		require.Error(t, run(t.Context(), args, &out))
		_, err := os.Stat(path)
		require.True(t, os.IsNotExist(err))
	}
}

func TestApplyFreshDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.sqlite")
	t.Setenv("XOLO_STORAGE_DATABASE_DSN", path)
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), []string{"apply", "-writers-stopped"}, &out))
	require.Contains(t, out.String(), `"applied": true`)
	db, err := openDatabase(path, true)
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, adapter.CheckDatabaseSchema(t.Context(), db))
}
