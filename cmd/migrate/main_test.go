package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

func TestOfflineMigrationWorkflow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	t.Setenv("XOLO_STORAGE_DATABASE_DSN", path)
	db, err := openDatabase(path, false)
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	store := adapter.NewStore(db)
	require.NoError(t, store.Migrate(t.Context()))
	require.NoError(t, db.Create(&adapter.Tenant{ID: "tenant-acme", Slug: "acme", Name: "Acme", Active: 1}).Error)
	require.NoError(t, db.Create(&adapter.User{ID: "user-one", TenantID: "tenant-acme", Email: "One@example.test"}).Error)
	require.NoError(t, db.Create(&adapter.User{ID: "user-two", TenantID: "tenant-acme", Provider: "oidc", Subject: "two", Email: "one@example.test"}).Error)
	require.NoError(t, db.Create(&adapter.User{
		ID: "user-deleted", TenantID: "tenant-acme", Provider: "oidc", Subject: "deleted", Email: "deleted@example.test",
	}).Error)
	require.NoError(t, db.Create(&adapter.Organization{ID: "org-acme", TenantID: "tenant-acme", Slug: "acme"}).Error)
	for _, id := range []string{"user-one", "user-deleted"} {
		require.NoError(t, db.Create(&adapter.Alert{ID: id, OrgID: "org-acme", OwnerID: id, Scope: "org"}).Error)
		require.NoError(t, db.Create(&adapter.InviteToken{ID: id, OrgID: "org-acme", CreatedByUserID: id, Role: "member"}).Error)
		require.NoError(t, db.Create(&adapter.PluginNodeSecret{
			ID: id, OrgID: "~:" + id, PluginName: "mcp-bridge", NodeID: "node", Key: "oauth:" + id, ValueEncrypted: "preserved",
		}).Error)
	}
	require.NoError(t, store.DeleteUser(t.Context(), model.UserID("user-deleted")))
	require.NoError(t, db.Table("events").Create(map[string]any{
		"id": "plugin-event", "org_id": "org-acme",
		"attributes": `{"actor_id":"user-two","tenant_user":"user-two"}`,
	}).Error)
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610020001").Error)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), []string{"diagnose"}, &out))
	var diagnostic adapter.RecoveryReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &diagnostic))
	require.Empty(t, diagnostic.Issues)
	require.Equal(t, []string{
		`unmapped event attribute: events.attributes [key "tenant_user"]: 1 distinct values; examples: "user-two"`,
	}, diagnostic.Notices)
	out.Reset()
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
	require.Equal(t, 2, artifact.Version)
	require.NoError(t, run(t.Context(), []string{"diagnose", "-plan", planPath}, &out))
	require.ErrorContains(t, run(t.Context(), []string{"apply", "-plan", planPath}, &out), "writers-stopped")
	require.NoError(t, run(t.Context(), []string{"apply", "-plan", planPath, "-writers-stopped"}, &out))
	require.NoError(t, run(t.Context(), []string{"apply", "-plan", planPath, "-writers-stopped"}, &out))
	require.NoError(t, run(t.Context(), []string{"diagnose"}, &out))
	var user adapter.User
	require.NoError(t, db.First(&user, "id = ?", artifact.IDs["users"]["user-two"]).Error)
	require.Equal(t, "one@example.test", user.Email)
	for _, id := range []string{"user-one", "user-deleted"} {
		want := id
		if next, ok := artifact.IDs["users"][id]; ok {
			want = next
		}
		var alert adapter.Alert
		require.NoError(t, db.First(&alert, "id = ?", id).Error)
		require.Equal(t, want, alert.OwnerID)
		var invite adapter.InviteToken
		require.NoError(t, db.First(&invite, "id = ?", id).Error)
		require.Equal(t, want, invite.CreatedByUserID)
		var secret adapter.PluginNodeSecret
		require.NoError(t, db.First(&secret, "id = ?", id).Error)
		require.Equal(t, "~:"+want, secret.OrgID)
		require.Equal(t, "oauth:"+want, secret.Key)
		require.Equal(t, "preserved", secret.ValueEncrypted)
	}

	var event adapter.Event
	require.NoError(t, db.First(&event, "id = ?", "plugin-event").Error)
	require.Equal(t, "user-two", (*event.Attributes.Val)["tenant_user"])
	require.Equal(t, artifact.IDs["users"]["user-two"], (*event.Attributes.Val)["actor_id"])
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

func TestCommandHelp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	t.Setenv("XOLO_STORAGE_DATABASE_DSN", path)
	for _, action := range []string{"diagnose", "plan", "apply"} {
		for _, help := range []string{"-h", "-help"} {
			t.Run(action+"/"+help, func(t *testing.T) {
				var out bytes.Buffer
				require.NoError(t, run(t.Context(), []string{action, help}, &out))
				require.Contains(t, out.String(), "Usage of "+action+":")
				require.NoFileExists(t, path)
			})
		}
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

func TestRecoveryPlanRejectsObsoleteOrUnknownDecisions(t *testing.T) {
	for _, tc := range []struct{ name, plan, message string }{
		{name: "version one", plan: `{"version":1,"email_overrides":{"old":"new@example.test"},"ids":{}}`, message: "regenerate with xolo-migrate plan"},
		{name: "unknown version", plan: `{"version":3,"ids":{}}`, message: "regenerate with xolo-migrate plan"},
		{name: "obsolete decision in v2", plan: `{"version":2,"tenant_owners":{},"ids":{}}`, message: "unknown field"},
		{name: "unknown correction field", plan: `{"version":2,"ids":{},"serialized_overrides":[{"typo":"ignored"}]}`, message: "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plan.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.plan), 0600))
			var out bytes.Buffer
			require.ErrorContains(t, run(t.Context(), []string{"apply", "-plan", path, "-writers-stopped"}, &out), tc.message)
		})
	}
}
