package pipeline_test

import (
	"context"
	"testing"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/pipeline"
	"github.com/xolo-gateway/xolo/internal/pipeline/pipelinetest"
)

// allowList is a pipeline.ModelAccess granting the listed names only.
type allowList map[string]bool

func (a allowList) CanUse(_ context.Context, name string) (bool, error) { return a[name], nil }

func modelGraph(proxyName string) *model.PipelineGraph {
	return pipelinetest.NewGraph().Generator("in").ModelWithProxy("m", proxyName).Sink("out").
		Edge("in", "request", "m", "request").Edge("m", "response", "out", "response").Build()
}

func accessEC(access pipeline.ModelAccess) pipeline.ExecutionContext {
	ec := pipelinetest.NewExecutionContext()
	ec.OrgSlug = "acme"
	ec.TeamID, ec.TeamSlug = "t1", "legal"
	ec.Access = access
	return ec
}

func requireAccessDenied(t *testing.T, err error, name string) {
	t.Helper()
	var denied *pipeline.ModelAccessError
	if !errors.As(err, &denied) {
		t.Fatalf("expected a model access error, got %v", err)
	}
	if denied.Name != name {
		t.Fatalf("denied %q, want %q", denied.Name, name)
	}
}

// A graph that delegates nothing, a personal virtual model, reaches only the
// models the request can use.
func TestAccessChecksEveryModelOfAPersonalGraph(t *testing.T) {
	h := pipelinetest.New()
	ctx := context.Background()

	_, err := h.Engine().RunForward(ctx, modelGraph("mistral"), accessEC(allowList{"gpt": true}))
	requireAccessDenied(t, err, "mistral")

	_, err = h.Engine().RunForward(ctx, modelGraph("gpt"), accessEC(allowList{"gpt": true}))
	if err != nil {
		t.Fatalf("an accessible model is reached: %v", err)
	}
}

// An organization graph delegates its static references, qualified or not,
// and the request's team segment does not change a name.
func TestAccessDelegatesStaticReferences(t *testing.T) {
	h := pipelinetest.New()
	ctx := context.Background()
	graph := modelGraph("acme/mistral")

	ec := accessEC(allowList{}).Delegating(graph)
	if _, err := h.Engine().RunForward(ctx, graph, ec); err != nil {
		t.Fatalf("a static reference is delegated: %v", err)
	}
	for _, name := range []string{"mistral", "acme/mistral", "acme/legal/mistral"} {
		if !ec.Delegates(name) {
			t.Errorf("%q designates the delegated reference", name)
		}
	}
	if ec.Delegates("other/mistral") {
		t.Error("a model of another organization is another reference")
	}
}

// A name reaching the model_name port at run time is checked, unless it is
// one of the graph's static references.
func TestAccessChecksDynamicNames(t *testing.T) {
	h := pipelinetest.New()
	ctx := context.Background()
	dynamic := func(name string) *model.PipelineGraph {
		graph := pipelinetest.NewGraph().Generator("in").Value("v", "string", name).Model("m").Sink("out").
			Edge("in", "request", "m", "request").Edge("v", "value", "m", "model_name").
			Edge("m", "response", "out", "response").Build()
		// A model_ref the author configured: "gpt" is a static reference.
		graph.Nodes = append(graph.Nodes, model.PipelineNode{ID: "ref", Type: model.NodeTypeModelRef, Data: pipelinetest.MustJSON(model.ModelRefNodeData{ProxyName: "gpt"})})
		return graph
	}

	graph := dynamic("mistral")
	_, err := h.Engine().RunForward(ctx, graph, accessEC(allowList{}).Delegating(graph))
	requireAccessDenied(t, err, "mistral")

	graph = dynamic("gpt")
	if _, err := h.Engine().RunForward(ctx, graph, accessEC(allowList{}).Delegating(graph)); err != nil {
		t.Fatalf("a dynamic name that is also a static reference is delegated: %v", err)
	}

	graph = dynamic("acme/other-team/gpt")
	_, err = h.Engine().RunForward(ctx, graph, accessEC(allowList{"gpt": true}).Delegating(graph))
	requireAccessDenied(t, err, "acme/other-team/gpt")
}

// Every candidate of a fallback chain is checked before it is called.
func TestAccessChecksFallbackCandidates(t *testing.T) {
	h := pipelinetest.New()
	graph := pipelinetest.NewGraph().Generator("in").ModelFallback("f", "mistral", "gpt").Sink("out").
		Edge("in", "request", "f", "request").Edge("f", "response", "out", "response").Build()

	_, err := h.Engine().RunForward(context.Background(), graph, accessEC(allowList{"gpt": true}))
	requireAccessDenied(t, err, "mistral")
}

// A middleware delegates its static references to the requests it wraps,
// the passthrough target being the requested model.
func TestAccessInMiddlewareChains(t *testing.T) {
	h := pipelinetest.New()
	mw := model.NewMiddleware("test-org", "classifier", "")
	mw.SetGraph(modelGraph("mistral"))

	ec := accessEC(allowList{}).Delegating(mw.Graph())
	ec.TargetModelName = "acme/gpt"
	if _, err := h.Engine().RunForward(context.Background(), mw.Graph(), ec); err != nil {
		t.Fatalf("the middleware reaches its static reference: %v", err)
	}
}
