package pipeline_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/pipeline"
	"github.com/xolo-gateway/xolo/internal/pipeline/pipelinetest"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// failingClient always fails. Mirrors the helper in model_fallback_executor_test.go.
type postRespFailingClient struct{}

func (postRespFailingClient) ChatCompletion(context.Context, ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	return nil, errUpstreamDown
}
func (postRespFailingClient) ChatCompletionStream(context.Context, ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	return nil, errUpstreamDown
}
func (postRespFailingClient) Embeddings(context.Context, []string, ...llm.EmbeddingsOptionFunc) (llm.EmbeddingsResponse, error) {
	return nil, nil
}
func (postRespFailingClient) Transcription(context.Context, []byte, ...llm.TranscriptionOptionFunc) (llm.TranscriptionResponse, error) {
	return nil, nil
}

var errUpstreamDown = errors.New("upstream down")

// TestPluginExecutor_BackwardDeadline hands a PostResponse cut by its
// deadline to the engine, which logs it as a timeout, and keeps any other
// failure of the plugin to itself.
func TestPluginExecutor_BackwardDeadline(t *testing.T) {
	failure := errors.New("plugin down")
	pluginClient := &pipelinetest.PluginClient{
		PostResponseFunc: func(ctx context.Context, _ *proto.PostResponseInput) (*proto.PostResponseOutput, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, failure
		},
	}
	executor := pipeline.NewPluginExecutor(pipelinetest.NewPluginProvider().
		Register("post-only", pipelinetest.PostOnlyDescriptor("post-only"), pluginClient))
	node := model.PipelineNode{ID: "post", Type: model.NodeTypePlugin, Data: pipelinetest.MustJSON(model.PluginNodeData{PluginName: "post-only"})}

	if _, err := executor.Backward(context.Background(), pipeline.BackwardInput{Node: node}); err != nil {
		t.Fatalf("a plugin failure must stay non-fatal, got %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if _, err := executor.Backward(ctx, pipeline.BackwardInput{Node: node}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a pass cut by its deadline must reach the engine, got %v", err)
	}
}

// TestPipeline_PostResponsePluginReceivesResolvedModelAfterFallback wires
// Generator -> Plugin(POST_RESPONSE) -> ModelFallback(primary fails, secondary
// answers) -> Sink and runs the full forward + backward cycle. The plugin's
// PostResponse RPC must see the model that actually answered, not the primary
// the forward pass resolved first.
//
// Regression for Conclave finding F2 (round 2, PR #89): without the
// ModelOutcome.UsedModel() preference in Engine.RunBackwardWithToolCalls, a
// ModelFallback chain would surface the primary candidate to billing/logging
// plugins even though the secondary answered.
func TestPipeline_PostResponsePluginReceivesResolvedModelAfterFallback(t *testing.T) {
	var postResponseInputs []*proto.PostResponseInput

	pluginClient := &pipelinetest.PluginClient{
		PostResponseFunc: func(_ context.Context, in *proto.PostResponseInput) (*proto.PostResponseOutput, error) {
			postResponseInputs = append(postResponseInputs, in)
			return &proto.PostResponseOutput{}, nil
		},
	}
	plugins := pipelinetest.NewPluginProvider().
		Register("post-only", pipelinetest.PostOnlyDescriptor("post-only"), pluginClient)

	// Primary fails, secondary answers with a fixed response.
	resolver := pipelinetest.NewModelResolver().
		WithModel("org/primary", postRespFailingClient{}).
		WithModel("org/secondary", pipelinetest.NewLLMClient("from secondary"))

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("post", "post-only").
		ModelFallback("fb", "org/primary", "org/secondary").
		Sink("sink").
		Edge("gen", "request", "post", "request").
		Edge("post", "request", "fb", "request").
		Edge("fb", "response", "sink", "response").
		Build()

	// Assemble the engine locally so we can register ModelFallback alongside
	// the standard node types without disturbing the shared harness.
	registry := pipeline.NewRegistry()
	registry.Register(model.NodeTypeGenerator, pipeline.NewGeneratorExecutor())
	registry.Register(model.NodeTypeSink, pipeline.NewSinkExecutor())
	registry.Register(model.NodeTypeValue, pipeline.NewValueExecutor())
	registry.Register(model.NodeTypePlugin, pipeline.NewPluginExecutor(plugins))
	registry.Register(model.NodeTypeModel, pipeline.NewModelExecutor(resolver, pipelinetest.NewVirtualModelStore(), pipeline.NewEngine(registry)))
	registry.Register(model.NodeTypeModelFallback, pipeline.NewModelFallbackExecutor(resolver))
	engine := pipeline.NewEngine(registry)

	forwardExec, err := engine.RunForward(context.Background(), graph, pipelinetest.NewExecutionContext())
	if err != nil {
		t.Fatalf("RunForward: %v", err)
	}

	// Drive the resolved client so ModelFallback marks the secondary as used.
	if _, err := forwardExec.ResolvedClient.ChatCompletion(context.Background()); err != nil {
		t.Fatalf("resolved client should have fallen back to secondary, got %v", err)
	}

	if forwardExec.ResolvedModel != "org/primary" {
		t.Fatalf("forward pass: ResolvedModel = %q, want %q (the eager-resolved primary)", forwardExec.ResolvedModel, "org/primary")
	}
	if forwardExec.ModelOutcome == nil {
		t.Fatal("ModelOutcome should be set by ModelFallbackExecutor")
	}
	if real, _, used := forwardExec.ModelOutcome.UsedModel(); !used || real != "org/secondary" {
		t.Fatalf("UsedModel() = (%q, _, %v), want (org/secondary, _, true)", real, used)
	}

	if _, err := engine.RunBackwardWithToolCalls(context.Background(), forwardExec, "from secondary", "", nil, false); err != nil {
		t.Fatalf("RunBackwardWithToolCalls: %v", err)
	}

	if len(postResponseInputs) != 1 {
		t.Fatalf("expected exactly 1 PostResponse invocation, got %d", len(postResponseInputs))
	}
	gotModel := postResponseInputs[0].GetModel()
	if gotModel != "org/secondary" {
		t.Errorf("PostResponseInput.Model = %q, want %q (the candidate that actually answered)", gotModel, "org/secondary")
	}
}

// TestPipeline_ToolProviderPlugin wires Generator -> Plugin(TOOL_PROVIDER) ->
// Model -> Sink and verifies that the resolved client (a) calls the
// plugin's CallTool RPC to resolve a matching tool_call, and (b) loops back
// to the underlying model client until a final answer is produced.
func TestPipeline_ToolProviderPlugin(t *testing.T) {
	var callToolInvocations []*proto.CallToolInput

	pluginClient := &pipelinetest.PluginClient{
		ListToolsFunc: func(_ context.Context, _ *proto.ListToolsInput) (*proto.ListToolsOutput, error) {
			return &proto.ListToolsOutput{
				Tools: []*proto.ToolDescriptor{
					{Name: "search", Description: "search the web"},
				},
			}, nil
		},
		CallToolFunc: func(_ context.Context, in *proto.CallToolInput) (*proto.CallToolOutput, error) {
			callToolInvocations = append(callToolInvocations, in)
			return &proto.CallToolOutput{ResultText: "42"}, nil
		},
	}

	plugins := pipelinetest.NewPluginProvider().
		Register("tool-bridge", pipelinetest.ToolProviderDescriptor("tool-bridge"), pluginClient)

	toolCall := llm.NewToolCall("call-1", "search", `{"q":"life, the universe and everything"}`)
	modelClient := &scriptedClient{responses: []*scriptedResponse{
		{toolCalls: []llm.ToolCall{toolCall}},
		{content: "the answer is 42"},
	}}
	resolver := pipelinetest.NewModelResolver().WithModel("org/gpt", modelClient)

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("tools", "tool-bridge").
		ModelWithProxy("mdl", "org/gpt").
		Sink("sink").
		Edge("gen", "request", "tools", "request").
		Edge("tools", "request", "mdl", "request").
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

	resp, err := exec.ResolvedClient.ChatCompletion(context.Background(), llm.WithMessages(llm.NewMessage(llm.RoleUser, "what is the answer?")))
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if resp.Message().Content() != "the answer is 42" {
		t.Errorf("expected final answer, got %q", resp.Message().Content())
	}
	if len(callToolInvocations) != 1 {
		t.Fatalf("expected exactly 1 CallTool invocation, got %d", len(callToolInvocations))
	}
	if callToolInvocations[0].Name != "search" {
		t.Errorf("expected CallTool for %q, got %q", "search", callToolInvocations[0].Name)
	}
	if len(modelClient.calls) != 2 {
		t.Errorf("expected 2 model calls (tool round + final), got %d", len(modelClient.calls))
	}
}

// TestPipeline_PostResponsePluginReceivesResolvedModel wires
// Generator -> Plugin(POST_RESPONSE) -> Model -> Sink and runs the full
// forward + backward cycle. The plugin's PostResponse RPC must see the
// identifier of the model that actually answered, not an empty string.
//
// Regression for https://github.com/xolo-gateway/xolo/issues/83: the plugin
// contract documents PostResponseInput.Model as "Model that was called",
// but PluginExecutor.Backward used to hardcode an empty string. A test that
// only hand-builds BackwardInput would have missed the bug, so this one
// drives the real Engine.RunBackward path.
func TestPipeline_PostResponsePluginReceivesResolvedModel(t *testing.T) {
	var postResponseInputs []*proto.PostResponseInput

	pluginClient := &pipelinetest.PluginClient{
		PostResponseFunc: func(_ context.Context, in *proto.PostResponseInput) (*proto.PostResponseOutput, error) {
			postResponseInputs = append(postResponseInputs, in)
			return &proto.PostResponseOutput{}, nil
		},
	}

	plugins := pipelinetest.NewPluginProvider().
		Register("post-only", pipelinetest.PostOnlyDescriptor("post-only"), pluginClient)

	modelClient := &scriptedClient{responses: []*scriptedResponse{
		{content: "hello back"},
	}}
	resolver := pipelinetest.NewModelResolver().WithModel("org/gpt", modelClient)

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("post", "post-only").
		ModelWithProxy("mdl", "org/gpt").
		Sink("sink").
		Edge("gen", "request", "post", "request").
		Edge("post", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	h := pipelinetest.New(
		pipelinetest.WithPlugins(plugins),
		pipelinetest.WithModelResolver(resolver),
	)

	result, err := h.Run(context.Background(), graph, pipelinetest.NewExecutionContext())
	if err != nil {
		t.Fatalf("harness.Run: %v", err)
	}

	if result.Forward == nil {
		t.Fatal("expected a forward execution, got nil")
	}
	if result.Forward.ResolvedModel != "org/gpt" {
		t.Fatalf("forward pass: ResolvedModel = %q, want %q", result.Forward.ResolvedModel, "org/gpt")
	}
	if len(postResponseInputs) != 1 {
		t.Fatalf("expected exactly 1 PostResponse invocation, got %d", len(postResponseInputs))
	}
	gotModel := postResponseInputs[0].GetModel()
	if gotModel != "org/gpt" {
		t.Errorf("PostResponseInput.Model = %q, want %q (the model the forward pass resolved)", gotModel, "org/gpt")
	}
}
