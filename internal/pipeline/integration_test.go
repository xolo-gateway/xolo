package pipeline_test

import (
	"context"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/pipeline/pipelinetest"
)

// ─────────────────────────────────────────
// Test 1: smart-model pipeline reconstitution
// ─────────────────────────────────────────

func TestPipeline_SmartModel(t *testing.T) {
	requestEval := pipelinetest.JSONPreRequest(func(_ map[string]any) map[string]any {
		return map[string]any{
			"complexity": 0.8,
			"level":      "high",
		}
	})

	fuzzyEval := pipelinetest.JSONPreRequest(func(inputs map[string]any) map[string]any {
		complexity, _ := inputs["complexity"].(float64)
		return map[string]any{"power_level": complexity}
	})

	// Fake script-processor: reads power_level from inputs, applies threshold
	router := pipelinetest.JSONPreRequest(func(inputs map[string]any) map[string]any {
		powerLevel, _ := inputs["power_level"].(float64)
		modelName := "org/gpt4-mini"
		if powerLevel > 0.7 {
			modelName = "org/claude"
		}
		return map[string]any{"model_name": modelName}
	})

	plugins := pipelinetest.NewPluginProvider().
		Register("complexity-scorer", pipelinetest.PreRequestDescriptor("complexity-scorer"), requestEval).
		Register("fuzzy-evaluator", pipelinetest.PreRequestDescriptor("fuzzy-evaluator"), fuzzyEval).
		Register("script-processor", pipelinetest.PreRequestDescriptor("script-processor"), router)

	resolver := pipelinetest.NewModelResolver().
		WithResponse("org/claude", "Hello from claude").
		WithResponse("org/gpt4-mini", "Hello from gpt4-mini")

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("req-eval", "complexity-scorer").
		Plugin("fuzzy", "fuzzy-evaluator").
		Plugin("router", "script-processor").
		Model("mdl").
		Sink("sink").
		Edge("gen", "request", "req-eval", "request").
		Edge("req-eval", "complexity", "fuzzy", "complexity").
		Edge("fuzzy", "power_level", "router", "power_level").
		Edge("router", "model_name", "mdl", "model_name").
		Edge("gen", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	h := pipelinetest.New(
		pipelinetest.WithPlugins(plugins),
		pipelinetest.WithModelResolver(resolver),
	)

	exec, err := h.Engine().RunForward(context.Background(), graph, pipelinetest.NewExecutionContext())
	if err != nil {
		t.Fatalf("RunForward failed: %v", err)
	}
	if exec.ResolvedClient == nil {
		t.Fatal("expected a resolved client, got nil")
	}
	// complexity=0.8 → power_level=0.8 > 0.7 → org/claude
	if exec.ResolvedModel != "org/claude" {
		t.Errorf("expected org/claude, got %q", exec.ResolvedModel)
	}
}

// ─────────────────────────────────────────
// Test 2: full request -> pipeline -> response cycle, with backward pass
// (pseudonymisation mock)
// ─────────────────────────────────────────

func TestPipeline_BackwardPass(t *testing.T) {
	const mapping = `{"John":"Person_A"}`

	pseudo := pipelinetest.JSONPreRequestWithState(func(_ map[string]any) (map[string]any, []byte) {
		return nil, []byte(mapping)
	})
	pseudo.PostResponseFunc = pipelinetest.PostResponseRewrite(func(_ []byte, content string) string {
		return strings.ReplaceAll(content, "Person_A", "John")
	})

	plugins := pipelinetest.NewPluginProvider().
		Register("pseudonymizer", pipelinetest.PrePostDescriptor("pseudonymizer"), pseudo)

	resolver := pipelinetest.NewModelResolver().
		WithResponse("org/gpt4", "Person_A est arrivé")

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("pseudo", "pseudonymizer").
		ModelWithProxy("mdl", "org/gpt4").
		Sink("sink").
		Edge("gen", "request", "pseudo", "request").
		Edge("pseudo", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	h := pipelinetest.New(
		pipelinetest.WithPlugins(plugins),
		pipelinetest.WithModelResolver(resolver),
	)

	result, err := h.Run(context.Background(), graph, pipelinetest.NewExecutionContext())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Rejected {
		t.Fatalf("unexpected rejection: %s", result.RejectionReason)
	}
	if result.FinalContent != "John est arrivé" {
		t.Errorf("expected 'John est arrivé', got %q", result.FinalContent)
	}
}

// ─────────────────────────────────────────
// Test 3: graph validation
// ─────────────────────────────────────────

func TestPipeline_GraphValidation(t *testing.T) {
	h := pipelinetest.New()

	t.Run("no_generator", func(t *testing.T) {
		graph := pipelinetest.NewGraph().Sink("sink").Build()
		_, err := h.Engine().RunForward(context.Background(), graph, pipelinetest.NewExecutionContext())
		if err == nil {
			t.Fatal("expected error for graph without generator")
		}
	})

	t.Run("no_sink", func(t *testing.T) {
		graph := pipelinetest.NewGraph().Generator("gen").Build()
		_, err := h.Engine().RunForward(context.Background(), graph, pipelinetest.NewExecutionContext())
		if err == nil {
			t.Fatal("expected error for graph without sink")
		}
	})

	t.Run("cycle", func(t *testing.T) {
		graph := pipelinetest.NewGraph().
			Generator("gen").
			Sink("a").
			Sink("b").
			Sink("sink").
			Edge("a", "out", "b", "in").
			Edge("b", "out", "a", "in").
			Build()
		_, err := h.Engine().RunForward(context.Background(), graph, pipelinetest.NewExecutionContext())
		if err == nil {
			t.Fatal("expected error for cyclic graph")
		}
	})
}

// ─────────────────────────────────────────
// Test 4: chaining VirtualModels
// ─────────────────────────────────────────

func TestPipeline_ChainedVirtualModels(t *testing.T) {
	resolver := pipelinetest.NewModelResolver().
		WithResponse("org/claude", "chained response")

	innerGraph := pipelinetest.NewGraph().
		Generator("gen").
		ModelWithProxy("mdl", "org/claude").
		Sink("sink").
		Edge("gen", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	innerVM := model.NewVirtualModel("test-org", "inner", "inner vm")
	innerVM.SetGraph(innerGraph)

	vmStore := pipelinetest.NewVirtualModelStore(innerVM)

	outerGraph := pipelinetest.NewGraph().
		Generator("gen").
		ModelWithProxy("mdl", "test-org/inner").
		Sink("sink").
		Edge("gen", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	h := pipelinetest.New(
		pipelinetest.WithModelResolver(resolver),
		pipelinetest.WithVirtualModelStore(vmStore),
	)

	exec, err := h.Engine().RunForward(context.Background(), outerGraph, pipelinetest.NewExecutionContext())
	if err != nil {
		t.Fatalf("RunForward failed: %v", err)
	}
	if exec.ResolvedClient == nil {
		t.Fatal("expected resolved client from chained VM")
	}
}

// ─────────────────────────────────────────
// Test: FinalMessagesJSON propagation from a plugin's modified_messages_json
// ─────────────────────────────────────────

func TestPipeline_FinalMessagesJSON(t *testing.T) {
	const modifiedMessages = `[{"role":"system","content":"injected"},{"role":"user","content":"hi"}]`

	plugins := pipelinetest.NewPluginProvider().
		Register("message-rewriter", pipelinetest.PreRequestDescriptor("message-rewriter"), pipelinetest.ModifiedMessages(modifiedMessages))

	resolver := pipelinetest.NewModelResolver().
		WithResponse("org/claude", "ok")

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("rewriter", "message-rewriter").
		ModelWithProxy("mdl", "org/claude").
		Sink("sink").
		Edge("gen", "request", "rewriter", "request").
		Edge("gen", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	h := pipelinetest.New(
		pipelinetest.WithPlugins(plugins),
		pipelinetest.WithModelResolver(resolver),
	)

	ec := pipelinetest.NewExecutionContext(
		pipelinetest.WithMessagesJSON(`[{"role":"user","content":"<system-reminder>foo</system-reminder>hi"}]`),
	)

	exec, err := h.Engine().RunForward(context.Background(), graph, ec)
	if err != nil {
		t.Fatalf("RunForward failed: %v", err)
	}
	if exec.FinalMessagesJSON != modifiedMessages {
		t.Errorf("FinalMessagesJSON = %q, want %q", exec.FinalMessagesJSON, modifiedMessages)
	}
}

func TestPipeline_CycleDetected(t *testing.T) {
	vmA := model.NewVirtualModel("test-org", "a", "vm A")
	vmAID := vmA.ID()

	h := pipelinetest.New(pipelinetest.WithVirtualModelStore(pipelinetest.NewVirtualModelStore(vmA)))

	graph := pipelinetest.NewGraph().
		Generator("gen").
		ModelWithProxy("mdl", "test-org/a").
		Sink("sink").
		Edge("gen", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	// vmA is already in VisitedVMs → cycle
	ec := pipelinetest.NewExecutionContext()
	ec.VisitedVMs[vmAID] = struct{}{}

	_, err := h.Engine().RunForward(context.Background(), graph, ec)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
}

// ─────────────────────────────────────────
// Test 5: VirtualModel without graph
// ─────────────────────────────────────────

func TestPipeline_ModelWithoutGraph(t *testing.T) {
	vmNoGraph := model.NewVirtualModel("test-org", "no-graph", "vm without graph")
	// vmNoGraph.Graph() == nil (no SetGraph called)

	h := pipelinetest.New(pipelinetest.WithVirtualModelStore(pipelinetest.NewVirtualModelStore(vmNoGraph)))

	graph := pipelinetest.NewGraph().
		Generator("gen").
		ModelWithProxy("mdl", "test-org/no-graph").
		Sink("sink").
		Edge("gen", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	_, err := h.Engine().RunForward(context.Background(), graph, pipelinetest.NewExecutionContext())
	if err == nil {
		t.Fatal("expected error when VM has no pipeline")
	}
}
