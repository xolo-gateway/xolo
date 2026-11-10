package gorm_test

import (
	"context"
	"strings"
	"testing"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	gormpkg "gorm.io/gorm"
)

func TestPluginUIConfigOnEachBackend(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()

		if got, err := store.GetPluginUIConfig(ctx, "org-1", "mcp-bridge"); err != nil || got != "" {
			t.Fatalf("expected nothing yet, got %q (%v)", got, err)
		}

		if err := store.SavePluginUIConfig(ctx, "org-1", "mcp-bridge", `{"a":1}`); err != nil {
			t.Fatalf("save: %v", err)
		}
		if err := store.SavePluginUIConfig(ctx, "org-1", "mcp-bridge", `{"a":2}`); err != nil {
			t.Fatalf("overwrite: %v", err)
		}
		if err := store.SavePluginUIConfig(ctx, "~:user-1", "mcp-bridge", `{"personal":true}`); err != nil {
			t.Fatalf("save personal: %v", err)
		}

		if got, _ := store.GetPluginUIConfig(ctx, "org-1", "mcp-bridge"); got != `{"a":2}` {
			t.Fatalf("expected the last save, got %q", got)
		}
		if got, _ := store.GetPluginUIConfig(ctx, "~:user-1", "mcp-bridge"); got != `{"personal":true}` {
			t.Fatalf("scopes mixed up, got %q", got)
		}
		if got, _ := store.GetPluginUIConfig(ctx, "org-1", "other"); got != "" {
			t.Fatalf("plugins mixed up, got %q", got)
		}

		// A real node config is far longer than the key columns.
		large := `{"prompt":"` + strings.Repeat("x", 64*1024) + `"}`
		if err := store.SavePluginUIConfig(ctx, "org-1", "system-prompt", large); err != nil {
			t.Fatalf("save large: %v", err)
		}
		if got, _ := store.GetPluginUIConfig(ctx, "org-1", "system-prompt"); got != large {
			t.Fatalf("large config not round-tripped (%d bytes back)", len(got))
		}
	})
}

// Deleting an organization or a user removes its handoff rows, like the
// other rows of its scope.
func TestPluginUIConfigFollowsHardDeletes(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, store)
		personal := "~:" + string(fixture.user)

		if err := store.SavePluginUIConfig(ctx, string(fixture.org), "mcp-bridge", `{"org":true}`); err != nil {
			t.Fatalf("save org: %v", err)
		}
		if err := store.SavePluginUIConfig(ctx, personal, "mcp-bridge", `{"me":true}`); err != nil {
			t.Fatalf("save personal: %v", err)
		}

		if err := store.DeleteOrg(ctx, fixture.org); err != nil {
			t.Fatalf("DeleteOrg: %v", err)
		}
		if got, _ := store.GetPluginUIConfig(ctx, string(fixture.org), "mcp-bridge"); got != "" {
			t.Fatalf("the deleted organization's handoff survived: %q", got)
		}

		if err := store.DeleteUser(ctx, fixture.user); err != nil {
			t.Fatalf("DeleteUser: %v", err)
		}
		if got, _ := store.GetPluginUIConfig(ctx, personal, "mcp-bridge"); got != "" {
			t.Fatalf("the deleted user's handoff survived: %q", got)
		}
	})
}
