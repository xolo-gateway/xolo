package gorm_test

import (
	"context"
	"strings"
	"testing"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
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
