package pipeline

import "github.com/xolo-gateway/xolo/internal/core/model"

// Registry maps node types to their executors.
type Registry struct {
	executors map[model.PipelineNodeType]NodeExecutor
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{executors: make(map[model.PipelineNodeType]NodeExecutor)}
}

// Register adds or replaces the executor for a node type.
func (r *Registry) Register(t model.PipelineNodeType, e NodeExecutor) {
	r.executors[t] = e
}

// Types returns the node types the registry executes.
func (r *Registry) Types() []model.PipelineNodeType {
	types := make([]model.PipelineNodeType, 0, len(r.executors))
	for t := range r.executors {
		types = append(types, t)
	}
	return types
}

// Get returns the executor for a node type.
func (r *Registry) Get(t model.PipelineNodeType) (NodeExecutor, bool) {
	e, ok := r.executors[t]
	return e, ok
}
