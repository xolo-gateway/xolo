package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

const userMessage = "Bonjour, peux-tu m'expliquer ce que veut dire l'acronyme RIB et où le trouver ?"

func runPreRequest(t *testing.T, config, messages string) string {
	t.Helper()
	out, err := (&Plugin{}).PreRequest(context.Background(), &proto.PreRequestInput{
		Ctx:          &proto.RequestContext{OrgId: "org-1", ConfigJson: config},
		MessagesJson: messages,
	})
	if err != nil {
		t.Fatalf("PreRequest returned error: %v", err)
	}
	if out == nil {
		t.Fatal("PreRequest returned nil output")
	}
	return out.GetModifiedMessagesJson()
}

// capturePluginLogs swaps slog's default handler for one that writes JSON
// records into a per-call buffer, and restores the previous default on
// return. The swap relies on Go's test model: tests using this helper must
// not call t.Parallel(), because slog.SetDefault mutates a process-global.
func capturePluginLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestPreRequest_DoesNotLogUserConversation pins the regression fixed by
// issue #104: the user-supplied conversation and the configured system prompt
// must not appear in any log record at any level, because PreRequest runs on
// the hot request path for every LLM call through the plugin.
func TestPreRequest_DoesNotLogUserConversation(t *testing.T) {
	rawMessages := `[{"role":"user","content":"` + userMessage + `"}]`
	config := `{"system_prompt":"` + secretPrompt + `","append":false}`

	buf := capturePluginLogs(t)
	out := runPreRequest(t, config, rawMessages)
	if out == "" {
		t.Fatal("expected a modified messages payload")
	}
	if !strings.Contains(out, secretPrompt) {
		t.Errorf("modified output should carry the configured prompt, got %q", out)
	}

	for _, rec := range logRecords(t, buf) {
		serialized, _ := json.Marshal(rec)
		s := string(serialized)
		if strings.Contains(s, userMessage) {
			t.Errorf("user message leaked into %v log record: %s", rec["level"], s)
		}
		if strings.Contains(s, secretPrompt) {
			t.Errorf("configured prompt leaked into %v log record: %s", rec["level"], s)
		}
		if strings.Contains(s, out) {
			t.Errorf("modified messages JSON leaked into %v log record: %s", rec["level"], s)
		}
	}
}

// TestPreRequest_InfoHasNoPayloads verifies the success log records the
// request with non-sensitive metrics only (counts and bytes), not the
// contents of the conversation.
func TestPreRequest_InfoHasNoPayloads(t *testing.T) {
	rawMessages := `[{"role":"user","content":"` + userMessage + `"},{"role":"user","content":"deuxième question"}]`
	config := `{"system_prompt":"` + secretPrompt + `","append":false}`

	buf := capturePluginLogs(t)
	runPreRequest(t, config, rawMessages)

	var found bool
	for _, rec := range logRecords(t, buf) {
		if rec["msg"] != "system-prompt: pre-request handled" {
			continue
		}
		found = true
		if rec["level"] != "INFO" {
			t.Errorf("expected INFO, got %v", rec["level"])
		}
		for _, key := range []string{"original_messages", "modified_messages"} {
			if _, ok := rec[key]; ok {
				t.Errorf("%s must not appear on any log record", key)
			}
		}
		if rec["original_messages_count"] == nil || rec["modified_messages_count"] == nil {
			t.Errorf("expected message counts on the INFO record, got %v", rec)
		}
		if rec["original_messages_bytes"] == nil || rec["modified_messages_bytes"] == nil {
			t.Errorf("expected message byte sizes on the INFO record, got %v", rec)
		}
	}
	if !found {
		t.Fatalf("no 'pre-request handled' log record found in: %s", buf.String())
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func decodeMessages(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var msgs []map[string]any
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		t.Fatalf("decode modified messages: %v", err)
	}
	return msgs
}

// TestPreRequest_EmptySystemPrompt verifies the plugin is a passthrough when
// no system prompt is configured.
func TestPreRequest_EmptySystemPrompt(t *testing.T) {
	input := mustMarshal(t, []map[string]string{
		{"role": "user", "content": "hello"},
	})
	out := runPreRequest(t, `{}`, input)
	if out != "" {
		t.Errorf("expected passthrough (no ModifiedMessagesJson) when system_prompt is empty, got %q", out)
	}
}

// TestPreRequest_MalformedMessagesJson_ReturnsPassthrough covers the case
// where the upstream MessagesJson cannot be parsed. Without an explicit
// error check, replace mode would silently emit a ModifiedMessagesJson that
// contains only the configured system message, replacing the entire client
// conversation. The plugin must instead pass the request through unchanged
// so the original body reaches the provider.
func TestPreRequest_MalformedMessagesJson_ReturnsPassthrough(t *testing.T) {
	p := &Plugin{}
	out, err := p.PreRequest(t.Context(), &proto.PreRequestInput{
		Ctx:          &proto.RequestContext{OrgId: "org1", ConfigJson: `{"system_prompt":"configured","append":false}`},
		MessagesJson: `{"role":"system"`, // intentionally malformed
	})
	if err != nil {
		t.Fatalf("PreRequest returned error: %v", err)
	}
	if !out.GetAllowed() {
		t.Errorf("expected Allowed=true on malformed MessagesJson, got false")
	}
	if got := out.GetModifiedMessagesJson(); got != "" {
		t.Errorf("expected empty ModifiedMessagesJson (passthrough), got %q", got)
	}
}

// TestPreRequest_ReplaceMode_DropsExistingSystemMessages is the regression
// test for the bug where "replace" mode prepended a new system message
// without removing the existing one(s), leaving the model with two prompts.
func TestPreRequest_ReplaceMode_DropsExistingSystemMessages(t *testing.T) {
	input := mustMarshal(t, []map[string]string{
		{"role": "system", "content": "client-provided prompt"},
		{"role": "user", "content": "hello"},
	})
	out := runPreRequest(t, `{"system_prompt":"configured prompt","append":false}`, input)
	msgs := decodeMessages(t, out)

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (configured system + user), got %d: %v", len(msgs), msgs)
	}
	if role, _ := msgs[0]["role"].(string); role != "system" {
		t.Errorf("expected first message role to be system, got %#v", msgs[0])
	}
	if content, _ := msgs[0]["content"].(string); content != "configured prompt" {
		t.Errorf("expected first message content to be the configured prompt, got %#v", msgs[0])
	}
	if role, _ := msgs[1]["role"].(string); role != "user" {
		t.Errorf("expected second message role to be user, got %#v", msgs[1])
	}
	if content, _ := msgs[1]["content"].(string); content != "hello" {
		t.Errorf("expected second message content to be hello, got %#v", msgs[1])
	}

	for i, m := range msgs {
		if role, _ := m["role"].(string); role == "system" {
			if content, _ := m["content"].(string); content == "client-provided prompt" {
				t.Errorf("system message at index %d should have been replaced, got %#v", i, m)
			}
		}
	}
}

// TestPreRequest_ReplaceMode_HandlesMultipleExistingSystemMessages ensures
// every existing system message is dropped, not only the first.
func TestPreRequest_ReplaceMode_HandlesMultipleExistingSystemMessages(t *testing.T) {
	input := mustMarshal(t, []map[string]string{
		{"role": "system", "content": "first client prompt"},
		{"role": "user", "content": "middle"},
		{"role": "system", "content": "second client prompt"},
	})
	out := runPreRequest(t, `{"system_prompt":"configured prompt","append":false}`, input)
	msgs := decodeMessages(t, out)

	systemCount := 0
	for _, m := range msgs {
		if role, _ := m["role"].(string); role == "system" {
			systemCount++
			if content, _ := m["content"].(string); content != "configured prompt" {
				t.Errorf("expected only the configured system prompt, got %#v", m)
			}
		}
	}
	if systemCount != 1 {
		t.Errorf("expected exactly 1 system message after replace, got %d: %v", systemCount, msgs)
	}
}

// TestPreRequest_ReplaceMode_NoExistingSystem covers the trivial case where
// no client system message is present: the configured one is still prepended.
func TestPreRequest_ReplaceMode_NoExistingSystem(t *testing.T) {
	input := mustMarshal(t, []map[string]string{
		{"role": "user", "content": "hello"},
	})
	out := runPreRequest(t, `{"system_prompt":"configured prompt","append":false}`, input)
	msgs := decodeMessages(t, out)

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %v", len(msgs), msgs)
	}
	if role, _ := msgs[0]["role"].(string); role != "system" {
		t.Errorf("expected system role first, got %#v", msgs[0])
	}
	if content, _ := msgs[0]["content"].(string); content != "configured prompt" {
		t.Errorf("expected configured system prompt first, got %#v", msgs[0])
	}
}

// TestPreRequest_AppendMode_Concatenates ensures append mode still merges
// with an existing system message instead of replacing it.
func TestPreRequest_AppendMode_Concatenates(t *testing.T) {
	input := mustMarshal(t, []map[string]string{
		{"role": "system", "content": "client-provided prompt"},
		{"role": "user", "content": "hello"},
	})
	out := runPreRequest(t, `{"system_prompt":"configured prompt","append":true}`, input)
	msgs := decodeMessages(t, out)

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %v", len(msgs), msgs)
	}
	if msgs[0]["role"] != "system" {
		t.Fatalf("expected system message first, got %#v", msgs[0])
	}
	content, _ := msgs[0]["content"].(string)
	if content != "client-provided prompt\n\nconfigured prompt" {
		t.Errorf("expected concatenated system prompt, got %q", content)
	}
}

// TestPreRequest_AppendMode_NoExistingSystem prepends the configured prompt
// when no client system message is there to merge with.
func TestPreRequest_AppendMode_NoExistingSystem(t *testing.T) {
	input := mustMarshal(t, []map[string]string{
		{"role": "user", "content": "hello"},
	})
	out := runPreRequest(t, `{"system_prompt":"configured prompt","append":true}`, input)
	msgs := decodeMessages(t, out)

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %v", len(msgs), msgs)
	}
	if role, _ := msgs[0]["role"].(string); role != "system" {
		t.Errorf("expected system role first, got %#v", msgs[0])
	}
	if content, _ := msgs[0]["content"].(string); content != "configured prompt" {
		t.Errorf("expected configured system prompt first, got %#v", msgs[0])
	}
}

// TestPreRequest_AppendMode_FoldsEverySystemMessage is the regression test
// for #149: append mode merged the configured prompt with the first client
// system message only and left the others in place, so the model still got
// several system prompts. Every client system prompt is now folded, in order,
// into a single leading system message ending with the configured prompt.
func TestPreRequest_AppendMode_FoldsEverySystemMessage(t *testing.T) {
	input := mustMarshal(t, []map[string]string{
		{"role": "system", "content": "first client prompt"},
		{"role": "user", "content": "middle"},
		{"role": "system", "content": "second client prompt"},
		{"role": "assistant", "content": "answer"},
	})
	out := runPreRequest(t, `{"system_prompt":"configured prompt","append":true}`, input)
	msgs := decodeMessages(t, out)

	want := []map[string]any{
		{"role": "system", "content": "first client prompt\n\nsecond client prompt\n\nconfigured prompt"},
		{"role": "user", "content": "middle"},
		{"role": "assistant", "content": "answer"},
	}
	assertMessages(t, msgs, want)
}

// TestPreRequest_AppendMode_MovesSystemMessageToHead checks that a client
// system message sent after other messages ends up at the head of the
// conversation: providers that accept a single system prompt want it first.
func TestPreRequest_AppendMode_MovesSystemMessageToHead(t *testing.T) {
	input := mustMarshal(t, []map[string]string{
		{"role": "user", "content": "hello"},
		{"role": "system", "content": "late client prompt"},
	})
	out := runPreRequest(t, `{"system_prompt":"configured prompt","append":true}`, input)
	msgs := decodeMessages(t, out)

	assertMessages(t, msgs, []map[string]any{
		{"role": "system", "content": "late client prompt\n\nconfigured prompt"},
		{"role": "user", "content": "hello"},
	})
}

// TestPreRequest_AppendMode_KeepsTextOfContentParts covers a client system
// message whose content is an array of content parts: its text used to be
// lost, the message ending up as "\n\nconfigured prompt". The parts are read
// as the proxy reads them: text parts (and bare strings) concatenated without
// a separator.
func TestPreRequest_AppendMode_KeepsTextOfContentParts(t *testing.T) {
	input := mustMarshal(t, []map[string]any{
		{"role": "system", "content": []any{
			map[string]any{"type": "text", "text": "part one, "},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/a.png"}},
			map[string]any{"type": "input_text", "text": "part two"},
			" and three",
		}},
		{"role": "user", "content": "hello"},
	})
	out := runPreRequest(t, `{"system_prompt":"configured prompt","append":true}`, input)
	msgs := decodeMessages(t, out)

	assertMessages(t, msgs, []map[string]any{
		{"role": "system", "content": "part one, part two and three\n\nconfigured prompt"},
		{"role": "user", "content": "hello"},
	})
}

// TestPreRequest_AppendMode_SkipsEmptySystemMessages checks that an empty
// client system message does not leave a leading separator in the result.
func TestPreRequest_AppendMode_SkipsEmptySystemMessages(t *testing.T) {
	input := mustMarshal(t, []map[string]string{
		{"role": "system", "content": ""},
		{"role": "user", "content": "hello"},
	})
	out := runPreRequest(t, `{"system_prompt":"configured prompt","append":true}`, input)
	msgs := decodeMessages(t, out)

	assertMessages(t, msgs, []map[string]any{
		{"role": "system", "content": "configured prompt"},
		{"role": "user", "content": "hello"},
	})
}

// TestPreRequest_BothModes_LeaveExactlyOneLeadingSystemMessage pins the
// invariant shared by both modes: whatever the client sends, the rewritten
// conversation holds exactly one system message, and it comes first.
func TestPreRequest_BothModes_LeaveExactlyOneLeadingSystemMessage(t *testing.T) {
	input := mustMarshal(t, []map[string]string{
		{"role": "user", "content": "one"},
		{"role": "system", "content": "a"},
		{"role": "system", "content": "b"},
		{"role": "user", "content": "two"},
		{"role": "system", "content": "c"},
	})
	for _, appendMode := range []bool{false, true} {
		config := mustMarshal(t, map[string]any{"system_prompt": "configured prompt", "append": appendMode})
		msgs := decodeMessages(t, runPreRequest(t, config, input))

		if len(msgs) != 3 {
			t.Fatalf("append=%v: expected 3 messages, got %d: %v", appendMode, len(msgs), msgs)
		}
		for i, m := range msgs {
			isSystem := m["role"] == "system"
			if isSystem != (i == 0) {
				t.Errorf("append=%v: expected a system message at index 0 only, got %#v at %d", appendMode, m, i)
			}
		}
	}
}

func assertMessages(t *testing.T, got, want []map[string]any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected %d messages, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		for _, key := range []string{"role", "content"} {
			if got[i][key] != want[i][key] {
				t.Errorf("message %d: expected %s %q, got %#v", i, key, want[i][key], got[i])
			}
		}
	}
}
