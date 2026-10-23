package pipeline_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/pipeline"
	"github.com/xolo-gateway/xolo/internal/pipeline/pipelinetest"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// TestPipeline_ToolProviderPrefetch is the end-to-end path of the prefetch: a
// tool provider plugin declares a conversation tool in its ListTools response,
// the executor registers it on the execution context, and the tool loop the
// graph builds resolves it before the model's first call.
func TestPipeline_ToolProviderPrefetch(t *testing.T) {
	var listToolsCalls int

	pluginClient := &pipelinetest.PluginClient{
		ListToolsFunc: func(_ context.Context, _ *proto.ListToolsInput) (*proto.ListToolsOutput, error) {
			listToolsCalls++
			return &proto.ListToolsOutput{
				Tools: []*proto.ToolDescriptor{
					{Name: "search", Description: "search the knowledge base"},
				},
				ConversationTools: []*proto.ToolConversation{{
					Trigger:        proto.ToolConversation_ALWAYS,
					Tool:           &proto.ToolDescriptor{Name: "search", Description: "search the knowledge base"},
					ArgumentsJson:  `{"query":"{{last_user_message}}"}`,
					Label:          "Knowledge base",
					ResultTemplate: "```\n{{result}}\n```",
				}},
			}, nil
		},
		CallToolFunc: func(_ context.Context, in *proto.CallToolInput) (*proto.CallToolOutput, error) {
			if !strings.Contains(in.GetArgumentsJson(), "reset my password") {
				t.Errorf("expected the query mapped from the user message, got %s", in.GetArgumentsJson())
			}
			return &proto.CallToolOutput{ResultText: "open the profile page"}, nil
		},
	}

	plugins := pipelinetest.NewPluginProvider().
		Register("mcp-bridge", pipelinetest.ToolProviderDescriptor("mcp-bridge"), pluginClient)

	modelClient := &scriptedClient{responses: []*scriptedResponse{{content: "here is how"}}}
	resolver := pipelinetest.NewModelResolver().WithModel("org/gpt", modelClient)

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("tools", "mcp-bridge").
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

	ec := pipelinetest.NewExecutionContext(pipelinetest.WithUserID("user-1"))
	ec.ConversationTools = pipeline.NewConversationTools()

	exec, err := h.Engine().RunForward(context.Background(), graph, ec)
	if err != nil {
		t.Fatalf("RunForward: %v", err)
	}
	if exec.ResolvedClient == nil {
		t.Fatal("expected a resolved client")
	}

	_, err = exec.ResolvedClient.ChatCompletion(context.Background(),
		llm.WithMessages(llm.NewMessage(llm.RoleUser, "I need to reset my password")),
	)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if listToolsCalls != 1 {
		t.Errorf("expected ListTools to be called once, was %d", listToolsCalls)
	}
	if len(modelClient.calls) == 0 {
		t.Fatal("the model was never called")
	}
	injected := lastMessage(modelClient.calls[0])
	if injected == nil || injected.Role() != llm.RoleUser {
		t.Fatalf("expected the injected context as a user message, got %v", injected)
	}
	for _, want := range []string{"Knowledge base", "```", "open the profile page"} {
		if !strings.Contains(injected.Content(), want) {
			t.Errorf("injected message %q does not contain %q", injected.Content(), want)
		}
	}
}

// TestPipeline_ToolProviderConversationValues checks the values a plugin read
// while listing (an MCP resource, say) reach the injected context too.
func TestPipeline_ToolProviderConversationValues(t *testing.T) {
	pluginClient := &pipelinetest.PluginClient{
		ListToolsFunc: func(_ context.Context, _ *proto.ListToolsInput) (*proto.ListToolsOutput, error) {
			return &proto.ListToolsOutput{
				Tools: []*proto.ToolDescriptor{},
				ConversationValues: []*proto.ConversationValue{{
					Label:   "MCP resource procedure",
					Content: "open a ticket first",
				}},
			}, nil
		},
	}

	plugins := pipelinetest.NewPluginProvider().
		Register("mcp-bridge", pipelinetest.ToolProviderDescriptor("mcp-bridge"), pluginClient)

	modelClient := &scriptedClient{responses: []*scriptedResponse{{content: "ok"}}}
	resolver := pipelinetest.NewModelResolver().WithModel("org/gpt", modelClient)

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("tools", "mcp-bridge").
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

	ec := pipelinetest.NewExecutionContext()
	ec.ConversationTools = pipeline.NewConversationTools()

	exec, err := h.Engine().RunForward(context.Background(), graph, ec)
	if err != nil {
		t.Fatalf("RunForward: %v", err)
	}
	if _, err := exec.ResolvedClient.ChatCompletion(context.Background(),
		llm.WithMessages(llm.NewMessage(llm.RoleUser, "hello")),
	); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	injected := lastMessage(modelClient.calls[0])
	if injected == nil || !strings.Contains(injected.Content(), "open a ticket first") {
		t.Fatalf("expected the conversation value injected, got %v", injected)
	}
}

// TestPipeline_TwoToolProvidersPrefetchOnce makes sure a graph with two
// tool-provider nodes does not run the prefetch twice: the tool loops nest, and
// without the once-per-execution guard each would inject the same results
// again, doubling both the cost and the context.
func TestPipeline_TwoToolProvidersPrefetchOnce(t *testing.T) {
	var calls int

	// Only ONE of the two providers declares a prefetch, so the assertion is
	// about the nesting of the tool loops and not about two plugins each asking
	// for their own tool: their results are both due, and both must be
	// injected.
	declaring := func(_ context.Context, _ *proto.ListToolsInput) (*proto.ListToolsOutput, error) {
		return &proto.ListToolsOutput{
			Tools: []*proto.ToolDescriptor{{Name: "search", Description: "searches"}},
			ConversationTools: []*proto.ToolConversation{{
				Trigger:       proto.ToolConversation_ALWAYS,
				Tool:          &proto.ToolDescriptor{Name: "search", Description: "searches"},
				ArgumentsJson: `{}`,
			}},
		}, nil
	}
	plain := func(_ context.Context, _ *proto.ListToolsInput) (*proto.ListToolsOutput, error) {
		return &proto.ListToolsOutput{
			Tools: []*proto.ToolDescriptor{{Name: "other", Description: "another tool"}},
		}, nil
	}
	callTool := func(_ context.Context, in *proto.CallToolInput) (*proto.CallToolOutput, error) {
		if in.GetName() == "search" {
			calls++
		}
		return &proto.CallToolOutput{ResultText: "found"}, nil
	}

	plugins := pipelinetest.NewPluginProvider().
		Register("first", pipelinetest.ToolProviderDescriptor("first"), &pipelinetest.PluginClient{
			ListToolsFunc: declaring,
			CallToolFunc:  callTool,
		}).
		Register("second", pipelinetest.ToolProviderDescriptor("second"), &pipelinetest.PluginClient{
			ListToolsFunc: plain,
			CallToolFunc:  callTool,
		})

	modelClient := &scriptedClient{responses: []*scriptedResponse{{content: "done"}}}
	resolver := pipelinetest.NewModelResolver().WithModel("org/gpt", modelClient)

	graph := pipelinetest.NewGraph().
		Generator("gen").
		Plugin("one", "first").
		Plugin("two", "second").
		ModelWithProxy("mdl", "org/gpt").
		Sink("sink").
		Edge("gen", "request", "one", "request").
		Edge("one", "request", "two", "request").
		Edge("two", "request", "mdl", "request").
		Edge("mdl", "response", "sink", "response").
		Build()

	h := pipelinetest.New(
		pipelinetest.WithPlugins(plugins),
		pipelinetest.WithModelResolver(resolver),
	)

	ec := pipelinetest.NewExecutionContext()
	ec.ConversationTools = pipeline.NewConversationTools()

	exec, err := h.Engine().RunForward(context.Background(), graph, ec)
	if err != nil {
		t.Fatalf("RunForward: %v", err)
	}
	if _, err := exec.ResolvedClient.ChatCompletion(context.Background(),
		llm.WithMessages(llm.NewMessage(llm.RoleUser, "hello")),
	); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if calls != 1 {
		t.Errorf("expected the prefetch to run once for two tool providers, ran %d times", calls)
	}
	// One injected block, not two: the model must not read the same result twice.
	injected := lastMessage(modelClient.calls[0])
	if injected == nil {
		t.Fatal("no injected message")
	}
	if got := strings.Count(injected.Content(), "found"); got != 1 {
		t.Errorf("expected the result injected once, found it %d times in %q", got, injected.Content())
	}
}
