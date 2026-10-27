package plugin

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
)

// TestPluginEnv_MaxMessageSize asserts that the plugin subprocess receives the
// host's message size bound: without it the plugin falls back to the default
// while the host applies another bound, and messages between the two fail.
func TestPluginEnv_MaxMessageSize(t *testing.T) {
	for _, tc := range []struct {
		configured, want int
	}{
		{16 << 20, 16 << 20},
		{0, pluginsdk.DefaultMaxMessageSize},
	} {
		m := NewManager(t.TempDir(), "", tc.configured, 0, nil, nil, nil, nil, "")
		entry := fmt.Sprintf("%s=%d", pluginsdk.MaxMessageSizeEnv, tc.want)
		if env := m.pluginEnv(context.Background()); !slices.Contains(env, entry) {
			t.Errorf("configured %d: environment lacks %q", tc.configured, entry)
		}
	}
}
