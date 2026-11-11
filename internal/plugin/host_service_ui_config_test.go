package plugin

import (
	"context"
	"sync"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

type sharedUIConfigs struct {
	mu   sync.Mutex
	rows map[string]string
}

func (s *sharedUIConfigs) SavePluginUIConfig(_ context.Context, scopeID, pluginName, configJSON string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[scopeID+"|"+pluginName] = configJSON
	return nil
}

func (s *sharedUIConfigs) GetPluginUIConfig(_ context.Context, scopeID, pluginName string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[scopeID+"|"+pluginName], nil
}

// The editor seeds through one instance, the plugin UI reads and saves through
// another, the editor reads back through a third: each step is a separate
// request a load balancer may send anywhere.
func TestUIConfigHandoffAcrossInstances(t *testing.T) {
	store := &sharedUIConfigs{rows: map[string]string{}}
	instances := make([]*XoloHostService, 3)
	for i := range instances {
		instances[i] = NewXoloHostService(nil, nil, nil, nil, "")
		instances[i].UseConfigStore(store)
	}
	ctx := context.Background()

	if err := instances[0].SeedConfig(ctx, "org-1", "mcp-bridge", `{"seeded":true}`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := instances[1].GetConfig(ctx, &proto.GetConfigRequest{OrgId: "org-1", PluginName: "mcp-bridge"})
	if err != nil || got.ConfigJson != `{"seeded":true}` {
		t.Fatalf("plugin UI did not see the seed: %q (%v)", got.GetConfigJson(), err)
	}

	if _, err := instances[1].SaveConfig(ctx, &proto.SaveConfigRequest{OrgId: "org-1", PluginName: "mcp-bridge", ConfigJson: `{"saved":true}`}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if cfg, err := instances[2].ReadConfig(ctx, "org-1", "mcp-bridge"); err != nil || cfg != `{"saved":true}` {
		t.Fatalf("editor did not read back the save: %q (%v)", cfg, err)
	}
	if cfg, _ := instances[2].ReadConfig(ctx, "org-2", "mcp-bridge"); cfg != "{}" {
		t.Fatalf("expected the empty default, got %q", cfg)
	}
}
