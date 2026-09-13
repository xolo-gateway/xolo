package proxy

import (
	"context"
	"testing"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

func TestApplyModifiedMessages_OpenAI(t *testing.T) {
	ec := pipeline.ExecutionContext{
		MessagesJSON: `[{"role":"user","content":"hi"}]`,
	}
	forwardExec := &pipeline.ForwardExecution{
		FinalMessagesJSON: `[{"role":"system","content":"injected"},{"role":"user","content":"hi"}]`,
	}
	req := &genaiProxy.ProxyRequest{Type: genaiProxy.RequestTypeChatCompletion}

	applyModifiedMessages(context.Background(), req, ec, forwardExec)

	if len(req.ChatOptions) != 1 {
		t.Fatalf("expected ChatOptions to be appended, got %d", len(req.ChatOptions))
	}

	opts := llm.NewChatCompletionOptions(req.ChatOptions...)
	if len(opts.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(opts.Messages))
	}
	if opts.Messages[0].Role() != llm.RoleSystem {
		t.Errorf("first message role = %q, want system", opts.Messages[0].Role())
	}
	if opts.Messages[0].Content() != "injected" {
		t.Errorf("first message content = %q, want injected", opts.Messages[0].Content())
	}
}

func TestApplyModifiedMessages_Anthropic(t *testing.T) {
	ec := pipeline.ExecutionContext{
		MessagesJSON: `[{"role":"user","content":"hi"}]`,
	}
	forwardExec := &pipeline.ForwardExecution{
		FinalMessagesJSON: `[{"role":"system","content":"injected"},{"role":"user","content":"hi"}]`,
	}
	req := &genaiProxy.ProxyRequest{Type: genaiProxy.RequestTypeMessage}

	applyModifiedMessages(context.Background(), req, ec, forwardExec)

	opts := llm.NewChatCompletionOptions(req.ChatOptions...)
	if len(opts.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(opts.Messages))
	}
	if opts.Messages[0].Role() != llm.RoleSystem {
		t.Errorf("first message role = %q, want system", opts.Messages[0].Role())
	}
}

func TestApplyModifiedMessages_NoChange(t *testing.T) {
	ec := pipeline.ExecutionContext{
		MessagesJSON: `[{"role":"user","content":"hi"}]`,
	}
	forwardExec := &pipeline.ForwardExecution{
		FinalMessagesJSON: ec.MessagesJSON,
	}
	req := &genaiProxy.ProxyRequest{Type: genaiProxy.RequestTypeChatCompletion}

	applyModifiedMessages(context.Background(), req, ec, forwardExec)

	if len(req.ChatOptions) != 0 {
		t.Errorf("expected no ChatOptions appended, got %d", len(req.ChatOptions))
	}
}

func TestApplyModifiedMessages_ConversionError(t *testing.T) {
	ec := pipeline.ExecutionContext{
		MessagesJSON: `[{"role":"user","content":"hi"}]`,
	}
	forwardExec := &pipeline.ForwardExecution{
		FinalMessagesJSON: `not json`,
	}
	req := &genaiProxy.ProxyRequest{Type: genaiProxy.RequestTypeChatCompletion}

	applyModifiedMessages(context.Background(), req, ec, forwardExec)

	if len(req.ChatOptions) != 0 {
		t.Errorf("expected no ChatOptions appended on conversion error, got %d", len(req.ChatOptions))
	}
}

func TestApplyModifiedMessages_AnthropicCarriesTopLevelSystem(t *testing.T) {
	ec := pipeline.ExecutionContext{
		MessagesJSON: `[{"role":"user","content":"hi"}]`,
	}
	forwardExec := &pipeline.ForwardExecution{
		FinalMessagesJSON: `[{"role":"system","content":"injected"},{"role":"user","content":"hi"}]`,
	}
	req := &genaiProxy.ProxyRequest{
		Type: genaiProxy.RequestTypeMessage,
		Body: []byte(`{"model":"m","system":"client policy","messages":[{"role":"user","content":"hi"}]}`),
	}

	applyModifiedMessages(context.Background(), req, ec, forwardExec)

	opts := llm.NewChatCompletionOptions(req.ChatOptions...)
	if len(opts.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(opts.Messages))
	}
	if opts.Messages[0].Role() != llm.RoleSystem || opts.Messages[0].Content() != "client policy" {
		t.Errorf("first message = (%q, %q), want the client system prompt", opts.Messages[0].Role(), opts.Messages[0].Content())
	}
	if opts.Messages[1].Content() != "injected" {
		t.Errorf("second message = %q, want the injected instruction", opts.Messages[1].Content())
	}
}

func TestApplyModifiedMessages_AnthropicSystemBlocks(t *testing.T) {
	ec := pipeline.ExecutionContext{
		MessagesJSON: `[{"role":"user","content":"hi"}]`,
	}
	forwardExec := &pipeline.ForwardExecution{
		FinalMessagesJSON: `[{"role":"user","content":"bonjour"}]`,
	}
	req := &genaiProxy.ProxyRequest{
		Type: genaiProxy.RequestTypeMessage,
		Body: []byte(`{"model":"m","system":[{"type":"text","text":"first"},{"type":"text","text":"second","cache_control":{"type":"ephemeral"}}]}`),
	}

	applyModifiedMessages(context.Background(), req, ec, forwardExec)

	opts := llm.NewChatCompletionOptions(req.ChatOptions...)
	if len(opts.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(opts.Messages))
	}
	if opts.Messages[0].Content() != "first" || opts.Messages[1].Content() != "second" {
		t.Errorf("system blocks = (%q, %q), want (first, second)", opts.Messages[0].Content(), opts.Messages[1].Content())
	}
}

func TestRequestSystemMessages_Absent(t *testing.T) {
	for name, body := range map[string]string{
		"empty body":   "",
		"no system":    `{"model":"m","messages":[]}`,
		"null system":  `{"model":"m","system":null}`,
		"empty string": `{"model":"m","system":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			msgs, err := requestSystemMessages([]byte(body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(msgs) != 0 {
				t.Errorf("messages = %d, want 0", len(msgs))
			}
		})
	}
}

func TestRequestSystemMessages_UnsupportedType(t *testing.T) {
	if _, err := requestSystemMessages([]byte(`{"system":42}`)); err == nil {
		t.Error("expected an error for a numeric system prompt")
	}
}
