package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// toolCallRewriter stands in for a node that restores its own rewriting on the
// response tool calls.
type toolCallRewriter struct {
	toolCalls string
}

func (e *toolCallRewriter) Forward(context.Context, model.PipelineNode, map[string]interface{}, ExecutionContext) (*ForwardResult, error) {
	return &ForwardResult{}, nil
}

func (e *toolCallRewriter) Backward(_ context.Context, _ BackwardInput) (*BackwardResult, error) {
	return &BackwardResult{ModifiedToolCallsJSON: e.toolCalls}, nil
}

// failingExecutor fails its backward pass, as a plugin whose process died does.
type failingExecutor struct{}

func (e *failingExecutor) Forward(context.Context, model.PipelineNode, map[string]interface{}, ExecutionContext) (*ForwardResult, error) {
	return &ForwardResult{}, nil
}

func (e *failingExecutor) Backward(_ context.Context, _ BackwardInput) (*BackwardResult, error) {
	return nil, errors.New("plugin is gone")
}

// A node failing its backward pass must not discard what an earlier node
// already restored: the response goes out with the work that succeeded rather
// than with placeholders the client cannot use.
func TestRunBackwardWithToolCalls_AFailingNodeKeepsWhatWasAlreadyRestored(t *testing.T) {
	registry := NewRegistry()
	registry.Register("rewrite", &toolCallRewriter{toolCalls: `[{"id":"call_1","name":"Read","arguments":"{\"path\":\"/home/Jean Dupont/notes.md\"}"}]`})
	registry.Register("broken", &failingExecutor{})
	engine := NewEngine(registry)

	// Backward runs in reverse, so "rewrite" is listed last to run first.
	exec := &ForwardExecution{
		ExecutedNodes: []ExecutedNode{
			{Node: model.PipelineNode{ID: "broken", Type: "broken"}},
			{Node: model.PipelineNode{ID: "rewrite", Type: "rewrite"}},
		},
	}

	outcome, err := engine.RunBackwardWithToolCalls(context.Background(), exec, "texte",
		`[{"id":"call_1","name":"Read","arguments":"{\"path\":\"/home/PERSON_1/notes.md\"}"}]`, nil, false)
	if err != nil {
		t.Fatalf("RunBackwardWithToolCalls() error = %v", err)
	}

	want := `[{"id":"call_1","name":"Read","arguments":"{\"path\":\"/home/Jean Dupont/notes.md\"}"}]`
	if outcome.ToolCallsJSON != want {
		t.Errorf("tool calls = %q, want %q", outcome.ToolCallsJSON, want)
	}
}

// When no node rewrites the calls, the outcome carries none: the caller then
// forwards the provider's own calls rather than a re-encoded copy of them.
func TestRunBackwardWithToolCalls_NoRewriteReportsNothing(t *testing.T) {
	registry := NewRegistry()
	registry.Register("rewrite", &toolCallRewriter{})
	engine := NewEngine(registry)

	exec := &ForwardExecution{
		ExecutedNodes: []ExecutedNode{{Node: model.PipelineNode{ID: "rewrite", Type: "rewrite"}}},
	}

	outcome, err := engine.RunBackwardWithToolCalls(context.Background(), exec, "texte",
		`[{"id":"call_1","name":"Read","arguments":"{}"}]`, nil, false)
	if err != nil {
		t.Fatalf("RunBackwardWithToolCalls() error = %v", err)
	}
	if outcome.ToolCallsJSON != "" {
		t.Errorf("tool calls = %q, want empty", outcome.ToolCallsJSON)
	}
}
