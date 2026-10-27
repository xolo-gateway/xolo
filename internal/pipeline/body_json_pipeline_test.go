package pipeline_test

import (
	"context"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/pipeline/pipelinetest"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// TestPipeline_BodyJSONFlowsToPreRequestInputModel is the end-to-end regression
// test for issue #34. It runs a Generator -> Plugin(PRE_REQUEST) -> Model ->
// Sink graph through Harness.Run with WithBodyJSON(...) seeding the EC, and
// asserts that PreRequestInput.Model equals the seeded body.
//
// What this test locks down:
//   - plugin_executor.go:156 reads ec.BodyJSON into PreRequestInput.Model.
//     A regression that reverted to ec.RequestJSON (no longer compiles) or to
//     "" would flip this test red.
//
// What this test does *not* lock down on its own:
//   - engine.go:77's seed step, which writes ec.BodyJSON into the
//     ValueContext under (generator-id, "request"), is *redundantly* covered:
//     GeneratorExecutor.Forward also writes ec.BodyJSON to its "request"
//     output (generator_executor.go:20), and the plugin's ResolveInputs reads
//     from the ValueContext after that overwrite. A regression in engine.go:77
//     alone is masked. TestGeneratorExecutor_RequestPortCarriesBodyJSON in
//     internal/adapter/proxy covers the generator's re-emit; together, the
//     two tests cover both writer sites.
func TestPipeline_BodyJSONFlowsToPreRequestInputModel(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hello"}],"temperature":0.7}`

	var capturedModel string
	capturePlugin := &pipelinetest.PluginClient{
		PreRequestFunc: func(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
			capturedModel = in.GetModel()
			return &proto.PreRequestOutput{Allowed: true}, nil
		},
	}

	plugins := pipelinetest.NewPluginProvider().
		Register("capture", pipelinetest.PreRequestDescriptor("capture"), capturePlugin)

	resolver := pipelinetest.NewModelResolver().WithResponse("org/gpt4", "ok")

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("capture", "capture").
		ModelWithProxy("mdl", "org/gpt4").
		Sink("sink").
		Edge("gen", "request", "capture", "request").
		Edge("capture", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	h := pipelinetest.New(
		pipelinetest.WithPlugins(plugins),
		pipelinetest.WithModelResolver(resolver),
	)

	ec := pipelinetest.NewExecutionContext(pipelinetest.WithBodyJSON(body))

	result, err := h.Run(context.Background(), graph, ec)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Rejected {
		t.Fatalf("unexpected rejection: %s", result.RejectionReason)
	}

	if capturedModel != body {
		t.Fatalf("PreRequestInput.Model = %q, want %q (full LLM request body sourced from ec.BodyJSON at plugin_executor.go:156)", capturedModel, body)
	}
}

// TestPipeline_PreRequestCarriesBodyOnce asserts that a PreRequest call holds
// the request body a single time: the messages it still carries untouched and
// the input port fed with the body are left out, and the SDK helpers restore
// them. A long conversation used to travel three times in the same message
// and exceed the gRPC message size bound.
func TestPipeline_PreRequestCarriesBodyOnce(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	messages := pluginsdk.BodyMessages(body)

	var captured *proto.PreRequestInput
	capturePlugin := &pipelinetest.PluginClient{
		PreRequestFunc: func(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
			captured = in
			return &proto.PreRequestOutput{Allowed: true}, nil
		},
	}

	plugins := pipelinetest.NewPluginProvider().
		Register("capture", pipelinetest.PreRequestDescriptor("capture"), capturePlugin)

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("capture", "capture").
		ModelWithProxy("mdl", "org/gpt4").
		Sink("sink").
		Edge("gen", "request", "capture", "request").
		Edge("capture", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	h := pipelinetest.New(
		pipelinetest.WithPlugins(plugins),
		pipelinetest.WithModelResolver(pipelinetest.NewModelResolver().WithResponse("org/gpt4", "ok")),
	)
	ec := pipelinetest.NewExecutionContext(
		pipelinetest.WithBodyJSON(body),
		pipelinetest.WithMessagesJSON(messages),
	)

	if _, err := h.Run(context.Background(), graph, ec); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if captured == nil {
		t.Fatal("PreRequest was not called")
	}

	if got := captured.GetMessagesJson(); got != "" {
		t.Errorf("MessagesJson = %q, want empty: the messages are those of the body", got)
	}
	if got := captured.GetInputsJson(); got != "{}" {
		t.Errorf("InputsJson = %q, want {}: the request port only repeats the body", got)
	}
	if got := captured.GetRequestInputPorts(); len(got) != 1 || got[0] != "request" {
		t.Errorf("RequestInputPorts = %v, want [request]", got)
	}

	if got := pluginsdk.PreRequestMessages(captured); got != messages {
		t.Errorf("PreRequestMessages = %q, want %q", got, messages)
	}
	inputs, err := pluginsdk.PreRequestInputs(captured)
	if err != nil {
		t.Fatalf("PreRequestInputs: %v", err)
	}
	if got := inputs["request"]; got != body {
		t.Errorf("restored request input = %v, want the body", got)
	}
}

// TestPipeline_PreRequestCarriesRewrittenMessages asserts that messages
// rewritten by an upstream node do travel, once, in MessagesJson, and that an
// input port fed with them is left out of InputsJson.
func TestPipeline_PreRequestCarriesRewrittenMessages(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	rewritten := `[{"role":"user","content":"bonjour"}]`

	rewriter := pipelinetest.JSONPreRequest(func(map[string]any) map[string]any {
		return map[string]any{"messages_json": rewritten}
	})

	var captured *proto.PreRequestInput
	capturePlugin := &pipelinetest.PluginClient{
		PreRequestFunc: func(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
			captured = in
			return &proto.PreRequestOutput{Allowed: true}, nil
		},
	}

	plugins := pipelinetest.NewPluginProvider().
		Register("rewriter", pipelinetest.PreRequestDescriptor("rewriter"), rewriter).
		Register("capture", pipelinetest.PreRequestDescriptor("capture"), capturePlugin)

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("rewriter", "rewriter").
		Plugin("capture", "capture").
		ModelWithProxy("mdl", "org/gpt4").
		Sink("sink").
		Edge("gen", "request", "rewriter", "request").
		Edge("rewriter", "messages_json", "capture", "messages_json").
		Edge("capture", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	h := pipelinetest.New(
		pipelinetest.WithPlugins(plugins),
		pipelinetest.WithModelResolver(pipelinetest.NewModelResolver().WithResponse("org/gpt4", "ok")),
	)
	ec := pipelinetest.NewExecutionContext(
		pipelinetest.WithBodyJSON(body),
		pipelinetest.WithMessagesJSON(pluginsdk.BodyMessages(body)),
	)

	if _, err := h.Run(context.Background(), graph, ec); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if captured == nil {
		t.Fatal("PreRequest was not called")
	}

	if got := captured.GetMessagesJson(); got != rewritten {
		t.Errorf("MessagesJson = %q, want the rewritten messages %q", got, rewritten)
	}
	if got := captured.GetInputsJson(); got != "{}" {
		t.Errorf("InputsJson = %q, want {}: the messages_json port only repeats MessagesJson", got)
	}
	if got := captured.GetMessagesInputPorts(); len(got) != 1 || got[0] != "messages_json" {
		t.Errorf("MessagesInputPorts = %v, want [messages_json]", got)
	}
	inputs, err := pluginsdk.PreRequestInputs(captured)
	if err != nil {
		t.Fatalf("PreRequestInputs: %v", err)
	}
	if got := inputs["messages_json"]; got != rewritten {
		t.Errorf("restored messages_json input = %v, want %q", got, rewritten)
	}
}

// TestPipeline_PreRequestDownstreamOfBodyRewrite asserts that a node fed a
// body rewritten upstream, as the pseudonymizer emits on its "request" port,
// receives that body as Model, never the original, and still carries it once.
func TestPipeline_PreRequestDownstreamOfBodyRewrite(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"Jean Dupont"}]}`
	rewrittenMessages := `[{"role":"user","content":"[PERSON_1]"}]`
	rewrittenBody := `{"model":"m","messages":` + rewrittenMessages + `}`

	rewriter := pipelinetest.JSONPreRequest(func(map[string]any) map[string]any {
		return map[string]any{"request": rewrittenBody, "messages_json": rewrittenMessages}
	})

	var captured *proto.PreRequestInput
	capturePlugin := &pipelinetest.PluginClient{
		PreRequestFunc: func(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
			captured = in
			return &proto.PreRequestOutput{Allowed: true}, nil
		},
	}

	plugins := pipelinetest.NewPluginProvider().
		Register("rewriter", pipelinetest.PreRequestDescriptor("rewriter"), rewriter).
		Register("capture", pipelinetest.PreRequestDescriptor("capture"), capturePlugin)

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("rewriter", "rewriter").
		Plugin("capture", "capture").
		ModelWithProxy("mdl", "org/gpt4").
		Sink("sink").
		Edge("gen", "request", "rewriter", "request").
		Edge("rewriter", "request", "capture", "request").
		Edge("rewriter", "messages_json", "capture", "messages_json").
		Edge("capture", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	h := pipelinetest.New(
		pipelinetest.WithPlugins(plugins),
		pipelinetest.WithModelResolver(pipelinetest.NewModelResolver().WithResponse("org/gpt4", "ok")),
	)
	ec := pipelinetest.NewExecutionContext(
		pipelinetest.WithBodyJSON(body),
		pipelinetest.WithMessagesJSON(pluginsdk.BodyMessages(body)),
	)

	if _, err := h.Run(context.Background(), graph, ec); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if captured == nil {
		t.Fatal("PreRequest was not called")
	}

	if got := captured.GetModel(); got != rewrittenBody {
		t.Errorf("Model = %q, want the rewritten body %q", got, rewrittenBody)
	}
	if got := captured.GetMessagesJson(); got != "" {
		t.Errorf("MessagesJson = %q, want empty: the messages are those of the rewritten body", got)
	}
	if got := captured.GetInputsJson(); got != "{}" {
		t.Errorf("InputsJson = %q, want {}", got)
	}
	if got := pluginsdk.PreRequestMessages(captured); got != rewrittenMessages {
		t.Errorf("PreRequestMessages = %q, want %q", got, rewrittenMessages)
	}
	inputs, err := pluginsdk.PreRequestInputs(captured)
	if err != nil {
		t.Fatalf("PreRequestInputs: %v", err)
	}
	if inputs["request"] != rewrittenBody || inputs["messages_json"] != rewrittenMessages {
		t.Errorf("restored inputs = %v, want the rewritten body and messages", inputs)
	}
}

// TestPipeline_ResolveModelCarriesBodyOnce asserts that a RESOLVE_MODEL call
// leaves out the messages its body holds, and that the SDK restores them.
func TestPipeline_ResolveModelCarriesBodyOnce(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	messages := pluginsdk.BodyMessages(body)

	var captured *proto.ResolveModelInput
	router := &pipelinetest.PluginClient{
		ResolveModelFunc: func(_ context.Context, in *proto.ResolveModelInput) (*proto.ResolveModelOutput, error) {
			captured = in
			return &proto.ResolveModelOutput{ResponseContent: "ok"}, nil
		},
	}
	descriptor := &proto.PluginDescriptor{
		Name:         "router",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_RESOLVE_MODEL},
	}

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("router", "router").
		Sink("sink").
		Edge("gen", "request", "router", "request").
		Edge("router", "response", "sink", "response").
		Build()

	h := pipelinetest.New(pipelinetest.WithPlugins(pipelinetest.NewPluginProvider().Register("router", descriptor, router)))
	ec := pipelinetest.NewExecutionContext(
		pipelinetest.WithBodyJSON(body),
		pipelinetest.WithMessagesJSON(messages),
	)

	if _, err := h.Run(context.Background(), graph, ec); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if captured == nil {
		t.Fatal("ResolveModel was not called")
	}

	if got := captured.GetBodyJson(); got != body {
		t.Errorf("BodyJson = %q, want the body", got)
	}
	if got := captured.GetMessagesJson(); got != "" {
		t.Errorf("MessagesJson = %q, want empty: the messages are those of the body", got)
	}
	if got := pluginsdk.ResolveModelMessages(captured); got != messages {
		t.Errorf("ResolveModelMessages = %q, want %q", got, messages)
	}
}

// TestPipeline_NestedPipelineReceivesRewrittenBody asserts that a body
// rewritten upstream of a virtual model node is the body the plugins of the
// virtual model's own graph receive: they must never see the original values.
func TestPipeline_NestedPipelineReceivesRewrittenBody(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"Jean Dupont"}]}`
	rewrittenMessages := `[{"role":"user","content":"[PERSON_1]"}]`
	rewrittenBody := `{"model":"m","messages":` + rewrittenMessages + `}`

	rewriter := pipelinetest.JSONPreRequest(func(map[string]any) map[string]any {
		return map[string]any{"request": rewrittenBody, "messages_json": rewrittenMessages}
	})

	var captured *proto.PreRequestInput
	capturePlugin := &pipelinetest.PluginClient{
		PreRequestFunc: func(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
			captured = in
			return &proto.PreRequestOutput{Allowed: true}, nil
		},
	}

	plugins := pipelinetest.NewPluginProvider().
		Register("rewriter", pipelinetest.PreRequestDescriptor("rewriter"), rewriter).
		Register("capture", pipelinetest.PreRequestDescriptor("capture"), capturePlugin)

	innerGraph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("capture", "capture").
		ModelWithProxy("mdl", "org/gpt4").
		Sink("sink").
		Edge("gen", "request", "capture", "request").
		Edge("capture", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()
	innerVM := model.NewVirtualModel("test-org", "inner", "inner vm")
	innerVM.SetGraph(innerGraph)

	outerGraph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("rewriter", "rewriter").
		ModelWithProxy("mdl", "test-org/inner").
		Sink("sink").
		Edge("gen", "request", "rewriter", "request").
		Edge("rewriter", "request", "mdl", "request").
		Edge("rewriter", "messages_json", "mdl", "messages_json").
		Edge("mdl", "response", "sink", "response").
		Build()

	h := pipelinetest.New(
		pipelinetest.WithPlugins(plugins),
		pipelinetest.WithModelResolver(pipelinetest.NewModelResolver().WithResponse("org/gpt4", "ok")),
		pipelinetest.WithVirtualModelStore(pipelinetest.NewVirtualModelStore(innerVM)),
	)
	ec := pipelinetest.NewExecutionContext(
		pipelinetest.WithBodyJSON(body),
		pipelinetest.WithMessagesJSON(pluginsdk.BodyMessages(body)),
	)

	if _, err := h.Engine().RunForward(context.Background(), outerGraph, ec); err != nil {
		t.Fatalf("RunForward failed: %v", err)
	}
	if captured == nil {
		t.Fatal("the virtual model's plugin was not called")
	}

	if got := captured.GetModel(); got != rewrittenBody {
		t.Errorf("Model = %q, want the rewritten body %q", got, rewrittenBody)
	}
	if got := captured.GetMessagesJson(); got != "" {
		t.Errorf("MessagesJson = %q, want empty: the messages are those of the rewritten body", got)
	}
	if got := pluginsdk.PreRequestMessages(captured); got != rewrittenMessages {
		t.Errorf("PreRequestMessages = %q, want %q", got, rewrittenMessages)
	}
}
