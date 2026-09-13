package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// preRequestWithMessages runs PreRequest on an arbitrary conversation, which
// the parts-based helper cannot express: `tool_calls` is a sibling of
// `content`, not one of its parts.
func preRequestWithMessages(t *testing.T, cfg Config, messages []map[string]any) *proto.PreRequestOutput {
	t.Helper()

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	messagesJSON, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}

	out, err := attachmentPlugin(t, cfg).PreRequest(context.Background(), &proto.PreRequestInput{
		Ctx:          &proto.RequestContext{OrgId: "org-1", ConfigJson: string(cfgJSON)},
		MessagesJson: string(messagesJSON),
	})
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	return out
}

// assistantToolCalls returns the tool_calls of the first assistant message.
func assistantToolCalls(t *testing.T, out *proto.PreRequestOutput) []any {
	t.Helper()

	var messages []map[string]any
	if err := json.Unmarshal([]byte(out.ModifiedMessagesJson), &messages); err != nil {
		t.Fatalf("unmarshal modified messages: %v", err)
	}
	for _, msg := range messages {
		if msg["role"] != "assistant" {
			continue
		}
		calls, ok := msg["tool_calls"].([]any)
		if !ok {
			t.Fatalf("assistant message has no tool_calls: %#v", msg)
		}
		return calls
	}
	t.Fatalf("no assistant message left in the request: %s", out.ModifiedMessagesJson)
	return nil
}

func TestPreRequest_OpenAIToolCallArgumentsArePseudonymized(t *testing.T) {
	cfg := attachmentConfig(t)

	out := preRequestWithMessages(t, cfg, []map[string]any{
		{"role": "user", "content": "Cherche le dossier."},
		{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []any{map[string]any{
				"id":       "call_1",
				"type":     "function",
				"function": map[string]any{"name": "Grep", "arguments": `{"pattern":"` + toolPartSecret + `","path":"/srv/dossiers"}`},
			}},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	if strings.Contains(out.ModifiedMessagesJson, toolPartSecret) {
		t.Fatalf("tool call arguments reached the provider in clear: %s", out.ModifiedMessagesJson)
	}

	call, _ := assistantToolCalls(t, out)[0].(map[string]any)
	if call["id"] != "call_1" || call["type"] != "function" {
		t.Errorf("id and type must be preserved: %#v", call)
	}
	fn, _ := call["function"].(map[string]any)
	if fn["name"] != "Grep" {
		t.Errorf("function name must be preserved: %#v", fn)
	}

	args, ok := fn["arguments"].(string)
	if !ok {
		t.Fatalf("arguments is not a string any more: %#v", fn["arguments"])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(args), &decoded); err != nil {
		t.Fatalf("arguments is no longer valid JSON (%v): %q", err, args)
	}
	if decoded["path"] != "/srv/dossiers" {
		t.Errorf("argument names and non-sensitive values must survive: %#v", decoded)
	}
	if pattern, _ := decoded["pattern"].(string); pattern == "" || strings.Contains(pattern, toolPartSecret) {
		t.Errorf("pattern = %q, want a placeholder", pattern)
	}
}

// The placeholders have to be the ones the rest of the conversation uses, or
// the backward pass restores nothing.
func TestPreRequest_ToolCallSharesTheSessionMapping(t *testing.T) {
	cfg := attachmentConfig(t)

	out := preRequestWithMessages(t, cfg, []map[string]any{
		{"role": "user", "content": "Écris à " + toolPartSecret + "."},
		{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"id":       "call_2",
				"type":     "function",
				"function": map[string]any{"name": "SendMail", "arguments": `{"to":"` + toolPartSecret + `"}`},
			}},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}

	var state pluginState
	if err := json.Unmarshal(out.NodeState, &state); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}

	var placeholder string
	for ph, value := range state.Mapping {
		if value == toolPartSecret {
			placeholder = ph
		}
	}
	if placeholder == "" {
		t.Fatalf("no placeholder recorded for the address: %+v", state.Mapping)
	}

	call, _ := assistantToolCalls(t, out)[0].(map[string]any)
	fn, _ := call["function"].(map[string]any)
	if args, _ := fn["arguments"].(string); !strings.Contains(args, placeholder) {
		t.Errorf("arguments = %q, want the shared placeholder %q", args, placeholder)
	}
}

// A message carrying tool_calls and no content at all must not be dropped.
func TestPreRequest_ToolCallMessageWithoutContentSurvives(t *testing.T) {
	cfg := attachmentConfig(t)

	out := preRequestWithMessages(t, cfg, []map[string]any{
		{"role": "user", "content": "Cherche."},
		{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"id":       "call_3",
				"type":     "function",
				"function": map[string]any{"name": "Read", "arguments": `{"path":"/tmp/x"}`},
			}},
		},
	})

	if !out.Allowed {
		t.Fatalf("request refused: %s", out.RejectionReason)
	}
	if len(assistantToolCalls(t, out)) != 1 {
		t.Errorf("the tool call was lost: %s", out.ModifiedMessagesJson)
	}
}

func TestAnonymizeToolCalls_Shapes(t *testing.T) {
	upper := func(s string) (string, error) { return strings.ToUpper(s), nil }

	t.Run("no tool calls", func(t *testing.T) {
		if _, changed, err := anonymizeToolCalls(nil, upper); err != nil || changed {
			t.Errorf("changed = %v, err = %v, want false and no error", changed, err)
		}
	})

	t.Run("arguments that are not JSON", func(t *testing.T) {
		calls, changed, err := anonymizeToolCalls([]any{map[string]any{
			"function": map[string]any{"name": "f", "arguments": "not json"},
		}}, upper)
		if err != nil || !changed {
			t.Fatalf("changed = %v, err = %v", changed, err)
		}
		fn, _ := calls[0].(map[string]any)["function"].(map[string]any)
		if fn["arguments"] != "NOT JSON" {
			t.Errorf("arguments = %#v, want the plain text rewritten", fn["arguments"])
		}
	})

	t.Run("html characters are not escaped", func(t *testing.T) {
		calls, _, err := anonymizeToolCalls([]any{map[string]any{
			"function": map[string]any{"name": "f", "arguments": `{"q":"a & b < c"}`},
		}}, func(s string) (string, error) { return s, nil })
		if err != nil {
			t.Fatal(err)
		}
		fn, _ := calls[0].(map[string]any)["function"].(map[string]any)
		if args, _ := fn["arguments"].(string); !strings.Contains(args, "a & b < c") {
			t.Errorf("arguments = %q, want the payload verbatim", args)
		}
	})

	t.Run("failure travels up", func(t *testing.T) {
		boom := func(string) (string, error) { return "", errors.New("anonymizer down") }
		if _, _, err := anonymizeToolCalls([]any{map[string]any{
			"function": map[string]any{"name": "f", "arguments": `{"q":"x"}`},
		}}, boom); err == nil {
			t.Error("expected the failure to travel up, got none")
		}
	})
}
