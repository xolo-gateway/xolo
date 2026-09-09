package pipeline

import (
	"context"
	"sync"
)

// ToolResultInspector inspects a single tool result the gateway fetched
// itself (e.g. from an MCP server) before it is fed back to the model. It is
// the check that PreRequest cannot perform, because the gateway resolves
// these tool calls inside the model loop, after the forward pass has run.
type ToolResultInspector interface {
	// InspectToolResult reports whether the result must block the request,
	// with a human reason. An error is treated as "do not block" by the
	// caller: a failing inspector must not take the gateway down.
	InspectToolResult(ctx context.Context, toolName, content string) (blocked bool, reason string, err error)
}

// ToolInspectorSet collects the inspectors contributed by the nodes of a
// single execution and runs them all on each tool result. It is shared by
// pointer through the ExecutionContext so that a node running early can
// register an inspector that the tool loop, built later, will use.
type ToolInspectorSet struct {
	mu         sync.Mutex
	inspectors []ToolResultInspector
}

// NewToolInspectorSet returns an empty set.
func NewToolInspectorSet() *ToolInspectorSet { return &ToolInspectorSet{} }

// Register adds an inspector. Nil inspectors and a nil set are ignored.
func (s *ToolInspectorSet) Register(i ToolResultInspector) {
	if s == nil || i == nil {
		return
	}
	s.mu.Lock()
	s.inspectors = append(s.inspectors, i)
	s.mu.Unlock()
}

// Empty reports whether no inspector is registered.
func (s *ToolInspectorSet) Empty() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inspectors) == 0
}

// Inspect runs every registered inspector on a tool result and returns the
// first block, with its reason. An inspector that errors is skipped, so a
// broken inspector fails open rather than closing the gateway.
func (s *ToolInspectorSet) Inspect(ctx context.Context, toolName, content string) (bool, string) {
	if s == nil {
		return false, ""
	}
	s.mu.Lock()
	inspectors := append([]ToolResultInspector(nil), s.inspectors...)
	s.mu.Unlock()
	for _, i := range inspectors {
		blocked, reason, err := i.InspectToolResult(ctx, toolName, content)
		if err != nil {
			continue
		}
		if blocked {
			return true, reason
		}
	}
	return false, ""
}
