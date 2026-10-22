package proxy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRegisteredNodeTypesKnown keeps the node types the web UI and the
// provisioning API accept in line with the ones the engine executes.
func TestRegisteredNodeTypesKnown(t *testing.T) {
	a := NewPipelineHookAdapter(nil, nil, nil, nil, nil, nil, nil)
	types := a.registry.Types()
	require.NotEmpty(t, types)
	for _, nodeType := range types {
		require.True(t, nodeType.Known(), "%s is executed but refused by validation", nodeType)
	}
}
