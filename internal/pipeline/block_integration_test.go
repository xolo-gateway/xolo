package pipeline_test

import (
	"context"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/pipeline/pipelinetest"
)

// A block node fed by a true boolean rejects the request before any model is
// resolved; fed by a false one, the pipeline runs as if the node were absent.
func TestPipeline_BlockNode(t *testing.T) {
	build := func(cond string) *pipelinetest.GraphBuilder {
		return pipelinetest.NewGraph().
			Generator("gen").
			Value("flag", "boolean", cond).
			Block("policy", "Hors périmètre (test).").
			ModelWithProxy("mdl", "org/gpt4").
			Sink("sink").
			Edge("flag", "value", "policy", "condition").
			Edge("gen", "request", "mdl", "request").
			Edge("mdl", "response", "sink", "response")
	}
	resolver := pipelinetest.NewModelResolver().WithResponse("org/gpt4", "réponse du modèle")
	h := pipelinetest.New(pipelinetest.WithModelResolver(resolver))

	t.Run("true rejects", func(t *testing.T) {
		result, err := h.Run(context.Background(), build("true").Build(), pipelinetest.NewExecutionContext())
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}
		if !result.Rejected {
			t.Fatalf("expected rejection, got content %q", result.FinalContent)
		}
		if result.RejectionReason != "Hors périmètre (test)." {
			t.Errorf("reason = %q", result.RejectionReason)
		}
	})

	t.Run("false lets through", func(t *testing.T) {
		result, err := h.Run(context.Background(), build("false").Build(), pipelinetest.NewExecutionContext())
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}
		if result.Rejected {
			t.Fatalf("unexpected rejection: %s", result.RejectionReason)
		}
		if result.FinalContent != "réponse du modèle" {
			t.Errorf("content = %q", result.FinalContent)
		}
	})
}

// A block node whose condition comes from the model output can never run,
// since the forward pass stops at the model. The engine refuses the graph
// instead of serving the request with the policy silently disabled.
func TestPipeline_BlockNodeBehindModelIsRefused(t *testing.T) {
	graph := pipelinetest.NewGraph().
		Generator("gen").
		ModelWithProxy("mdl", "org/gpt4").
		Block("policy", "").
		Sink("sink").
		Edge("gen", "request", "mdl", "request").
		Edge("mdl", "response", "policy", "condition").
		Edge("mdl", "response", "sink", "response").
		Build()
	resolver := pipelinetest.NewModelResolver().WithResponse("org/gpt4", "réponse du modèle")
	h := pipelinetest.New(pipelinetest.WithModelResolver(resolver))

	_, err := h.Run(context.Background(), graph, pipelinetest.NewExecutionContext())
	if err == nil || !strings.Contains(err.Error(), "policy") {
		t.Fatalf("expected an error naming the block node, got %v", err)
	}
}
