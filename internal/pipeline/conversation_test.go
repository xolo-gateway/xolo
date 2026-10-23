package pipeline_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

// prefetchTool builds a tool recording its calls, answering with a text that
// echoes the arguments it was called with.
type prefetchTool struct {
	tool  llm.Tool
	calls []map[string]any
}

func newPrefetchTool(name string) *prefetchTool {
	p := &prefetchTool{}
	p.tool = llm.NewFuncTool(name, "looks things up", map[string]any{"type": "object"}, func(_ context.Context, params map[string]any) (llm.ToolResult, error) {
		p.calls = append(p.calls, params)
		encoded, _ := json.Marshal(params)
		return llm.NewToolResult("result of " + name + " for " + string(encoded)), nil
	})
	return p
}

func conversationToolSet(t *testing.T, tool llm.Tool, arguments, label, template string) *pipeline.ConversationTools {
	t.Helper()
	set := pipeline.NewConversationTools()
	set.Tools.Register(pipeline.ConversationTool{
		Tool:           tool,
		Arguments:      arguments,
		Label:          label,
		ResultTemplate: template,
	})
	return set
}

func contextWithUser(message string) []llm.ChatCompletionOptionFunc {
	return []llm.ChatCompletionOptionFunc{
		llm.WithMessages(
			llm.NewMessage(llm.RoleSystem, "be helpful"),
			llm.NewMessage(llm.RoleUser, message),
		),
	}
}

// TestToolLoop_PrefetchRunsBeforeFirstModelCall is the first acceptance
// criterion: with a prefetch tool, the tool runs once before the first model
// call, on every request, and its result is in the conversation the model sees.
func TestToolLoop_PrefetchRunsBeforeFirstModelCall(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "stream"}[stream], func(t *testing.T) {
			prefetch := newPrefetchTool("search")
			inner := &scriptedClient{responses: []*scriptedResponse{
				{content: "the answer"},
				{content: "the answer again"},
			}}

			ec := pipeline.ExecutionContext{OrgID: "org", UserID: "user"}

			// Twice: the criterion is "on every request", not once per client.
			// Each request builds its own ExecutionContext, as the proxy hook
			// adapter does, so the set is fresh per request.
			for i := range 2 {
				c := pipeline.NewToolLoopClient(inner, []llm.Tool{prefetch.tool}, 0, 0, nil).
					WithConversationTools(
						conversationToolSet(t, prefetch.tool, `{"query":"{{last_user_message}}"}`, "Docs", "[docs]\n{{result}}"),
						ec,
					)

				if stream {
					ch, err := c.ChatCompletionStream(context.Background(), contextWithUser("how do I reset it?")...)
					if err != nil {
						t.Fatalf("request %d: ChatCompletionStream: %v", i, err)
					}
					for range ch {
					}
				} else {
					if _, err := c.ChatCompletion(context.Background(), contextWithUser("how do I reset it?")...); err != nil {
						t.Fatalf("request %d: ChatCompletion: %v", i, err)
					}
				}
			}

			if len(prefetch.calls) != 2 {
				t.Fatalf("expected the tool to run once per request (2), ran %d times", len(prefetch.calls))
			}
			if got := prefetch.calls[0]["query"]; got != "how do I reset it?" {
				t.Errorf("expected the query mapped from the last user message, got %v", got)
			}

			// The model's first call must already carry the injected result.
			if len(inner.calls) == 0 {
				t.Fatal("the model was never called")
			}
			first := lastMessage(inner.calls[0])
			if first == nil {
				t.Fatal("the first model call carried no messages")
			}
			if first.Role() != llm.RoleUser {
				t.Errorf("expected the injected context as a user message, got role %q", first.Role())
			}
			for _, want := range []string{"Docs", "[docs]", "result of search"} {
				if !strings.Contains(first.Content(), want) {
					t.Errorf("injected message %q does not contain %q", first.Content(), want)
				}
			}
		})
	}
}

// TestToolLoop_PrefetchSkippedWithoutUserMessage makes sure a prefetch whose
// arguments cannot be mapped is not sent as an empty query.
func TestToolLoop_PrefetchSkippedWithoutUserMessage(t *testing.T) {
	prefetch := newPrefetchTool("search")
	inner := &scriptedClient{responses: []*scriptedResponse{{content: "the answer"}}}

	c := pipeline.NewToolLoopClient(inner, []llm.Tool{prefetch.tool}, 0, 0, nil).
		WithConversationTools(
			conversationToolSet(t, prefetch.tool, `{"query":"{{last_user_message}}"}`, "", ""),
			pipeline.ExecutionContext{},
		)

	// No user message at all: nothing to map a query from.
	if _, err := c.ChatCompletion(context.Background(), llm.WithMessages(llm.NewMessage(llm.RoleSystem, "hi"))); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if len(prefetch.calls) != 0 {
		t.Errorf("expected no call without a query to map, got %v", prefetch.calls)
	}

	// No conversation tools at all: the loop behaves as it always did.
	plain := &scriptedClient{responses: []*scriptedResponse{{content: "ok"}}}
	loop := pipeline.NewToolLoopClient(plain, nil, 0, 0, nil)
	if _, err := loop.ChatCompletion(context.Background(), contextWithUser("hello")...); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got := len(plain.calls[0]); got != 2 {
		t.Errorf("expected the 2 request messages untouched, got %d", got)
	}
}

// TestToolLoop_PrefetchFailureIsInjected makes sure a failing prefetch leaves
// the model able to answer instead of failing the request.
func TestToolLoop_PrefetchFailureIsInjected(t *testing.T) {
	failing := llm.NewFuncTool("search", "looks things up", nil, func(context.Context, map[string]any) (llm.ToolResult, error) {
		return nil, errors.New("the MCP server is down")
	})
	inner := &scriptedClient{responses: []*scriptedResponse{{content: "the answer"}}}

	c := pipeline.NewToolLoopClient(inner, nil, 0, 0, nil).
		WithConversationTools(conversationToolSet(t, failing, "", "", ""), pipeline.ExecutionContext{})

	if _, err := c.ChatCompletion(context.Background(), contextWithUser("hello")...); err != nil {
		t.Fatalf("a failing prefetch must not fail the request: %v", err)
	}
	if msg := lastMessage(inner.calls[0]); msg == nil || !strings.Contains(msg.Content(), "the tool failed") {
		t.Errorf("expected the failure reported to the model, got %v", msg)
	}
}

// TestToolLoop_PrefetchResultInspection checks the prefetched result goes
// through the tool-result inspectors, like a result the model asked for.
func TestToolLoop_PrefetchResultInspection(t *testing.T) {
	prefetch := newPrefetchTool("search")
	inner := &scriptedClient{responses: []*scriptedResponse{{content: "the answer"}}}

	set := pipeline.NewToolInspectorSet()
	set.Register(alwaysBlocker{reason: "poisoned context"})

	c := pipeline.NewToolLoopClient(inner, nil, 0, 0, set).
		WithConversationTools(conversationToolSet(t, prefetch.tool, "", "", ""), pipeline.ExecutionContext{})

	_, err := c.ChatCompletion(context.Background(), contextWithUser("hello")...)
	var rejection *pipeline.RejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("expected a RejectionError, got %v", err)
	}
	if rejection.Reason != "poisoned context" {
		t.Errorf("expected the inspector's reason, got %q", rejection.Reason)
	}
	if len(inner.calls) != 0 {
		t.Error("the model must not be called once the prefetched context was blocked")
	}
}

// TestFillTemplate covers the placeholders a plugin may use.
func TestFillTemplate(t *testing.T) {
	values := map[string]string{
		"last_user_message": "what is X?",
		"user_id":           "u1",
	}

	tests := []struct {
		template string
		want     string
		missing  bool
	}{
		{`{"q":"{{last_user_message}}"}`, `{"q":"what is X?"}`, false},
		{`{{ user_id }}`, "u1", false}, // surrounding spaces are ignored
		{`{{org_id}}`, `{{org_id}}`, true},
		{`{{result}}`, `{{result}}`, true}, // not a request value
		{`{"q":"{{last_user_message}}","u":"{{user_id}}"}`, `{"q":"what is X?","u":"u1"}`, false},
		// Values are never expanded again.
		{`{{user_id}}`, "u1", false},
	}
	for _, tt := range tests {
		got, missing := pipeline.FillTemplate(tt.template, values)
		if got != tt.want || missing != tt.missing {
			t.Errorf("FillTemplate(%q) = (%q, %v), want (%q, %v)", tt.template, got, missing, tt.want, tt.missing)
		}
	}
}

func TestFillTemplate_DoesNotExpandValues(t *testing.T) {
	values := map[string]string{"user_id": "{{org_id}}", "org_id": "org"}
	got, missing := pipeline.FillTemplate("{{user_id}}", values)
	if got != "{{org_id}}" || missing {
		t.Errorf("expected the value injected verbatim, got (%q, %v)", got, missing)
	}
}

// lastMessage returns the last message of a conversation, nil when empty.
func lastMessage(messages []llm.Message) llm.Message {
	if len(messages) == 0 {
		return nil
	}
	return messages[len(messages)-1]
}

// alwaysBlocker blocks every result it inspects.
type alwaysBlocker struct{ reason string }

func (b alwaysBlocker) InspectToolResult(context.Context, string, string) (bool, string, error) {
	return true, b.reason, nil
}
