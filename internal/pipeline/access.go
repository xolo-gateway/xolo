package pipeline

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// ModelAccess decides whether the request running a pipeline may reach a
// model by name.
type ModelAccess interface {
	// CanUse reports whether the request may use the model name designates,
	// as a node gives it: bare, qualified with the organization, with the
	// request's team, or a personal "~/" name.
	CanUse(ctx context.Context, name string) (bool, error)
}

// ModelAccessError reports a model a pipeline reached without access.
type ModelAccessError struct {
	Name string
}

func (e *ModelAccessError) Error() string {
	return "model '" + e.Name + "' not available to this request"
}

// checkModelAccess allows a model name reached by a node: one of the static
// references of a delegating graph, or a model the request can use itself.
func (ec ExecutionContext) checkModelAccess(ctx context.Context, name string) error {
	if ec.Access == nil {
		return nil
	}
	if _, delegated := ec.Delegated[ec.refKey(name)]; delegated {
		return nil
	}
	ok, err := ec.Access.CanUse(ctx, name)
	if err != nil {
		return errors.WithStack(err)
	}
	if !ok {
		return errors.WithStack(&ModelAccessError{Name: name})
	}
	return nil
}

// Delegates reports whether name is a static reference of the delegating
// graph being run, which the request may reach without access of its own.
func (ec ExecutionContext) Delegates(name string) bool {
	_, ok := ec.Delegated[ec.refKey(name)]
	return ok
}

// Delegating returns the context for running graph as a graph that delegates
// its static references: an organization virtual model or a middleware.
func (ec ExecutionContext) Delegating(graph *model.PipelineGraph) ExecutionContext {
	return ec.delegating(graph)
}

// delegating returns the context for running graph as a graph that delegates
// its static references: an organization virtual model or a middleware.
func (ec ExecutionContext) delegating(graph *model.PipelineGraph) ExecutionContext {
	ec.Delegated = map[string]struct{}{}
	for _, ref := range model.StaticModelRefs(graph) {
		ec.Delegated[ec.refKey(ref)] = struct{}{}
	}
	return ec
}

// checking returns the context for running a graph that delegates nothing, a
// personal virtual model.
func (ec ExecutionContext) checking() ExecutionContext {
	ec.Delegated = nil
	return ec
}

// refKey normalizes a model name for the delegation set: a name of the
// request's organization, bare or qualified, its team segment dropped, keys
// on its local name; another organization's on its qualified name.
func (ec ExecutionContext) refKey(name string) string {
	n, ok := ec.parseModelName(name)
	if !ok {
		return name
	}
	if n.Org == "" || n.Org == ec.OrgSlug {
		return n.Local
	}
	return n.OrgLevel().String()
}

// withoutTeam drops the request's team segment from a model name.
func (ec ExecutionContext) withoutTeam(name string) string {
	n, ok := ec.parseModelName(name)
	if !ok {
		return name
	}
	return n.OrgLevel().String()
}

// parseModelName parses a model name a node gives. The only team segment it
// reads is the request's: a node naming another team names nothing it can
// reach, and the resolution fails.
func (ec ExecutionContext) parseModelName(name string) (model.ModelName, bool) {
	return model.ParseModelName(name, func(org, segment string) bool {
		return org == ec.OrgSlug && ec.TeamSlug != "" && segment == ec.TeamSlug
	})
}
