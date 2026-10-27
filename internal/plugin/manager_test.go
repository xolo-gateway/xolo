package plugin_test

import (
	"context"
	"os"
	"testing"

	"github.com/xolo-gateway/xolo/internal/plugin"
)

func TestPluginManager_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := plugin.NewManager(dir, "", 0, 0, nil, nil, nil, nil, "")
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Shutdown()

	plugins := mgr.List()
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins in empty dir, got %d", len(plugins))
	}
}

func TestPluginManager_MissingDir(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := plugin.NewManager("/nonexistent/path/xolo/plugins", "", 0, 0, nil, nil, nil, nil, "")
	// Should not error — missing dir is treated as empty
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start with missing dir: %v", err)
	}
	defer mgr.Shutdown()
}

func TestPluginManager_IgnoresNonExecutable(t *testing.T) {
	dir := t.TempDir()
	// Create a non-executable file
	f, err := os.CreateTemp(dir, "notexec")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	os.Chmod(f.Name(), 0644)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := plugin.NewManager(dir, "", 0, 0, nil, nil, nil, nil, "")
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Shutdown()

	if len(mgr.List()) != 0 {
		t.Error("expected non-executable file to be ignored")
	}
}

func TestManager_HTTPPort_UnknownPlugin_ReturnsZero(t *testing.T) {
	m := plugin.NewManager("/nonexistent/path/xolo/plugins", "", 0, 0, nil, nil, nil, nil, "")
	if p := m.HTTPPort("unknown-plugin"); p != 0 {
		t.Errorf("expected 0 for unknown plugin, got %d", p)
	}
}
