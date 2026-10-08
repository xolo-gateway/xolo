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
		// DEBUG is the verbose level where full payloads are kept by
		// design (issue #104 reduces them to counts/sizes at INFO, but
		// the full JSON stays available at DEBUG for incident
		// investigation). The production logger is INFO, so a
		// regression that promotes a payload to INFO would break this
		// assertion and is the class of bug #122 and #104 closed.
		if rec["level"] == "DEBUG" {
			continue
		}
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
				t.Errorf("%s must not appear on the INFO record (only DEBUG, off by default)", key)
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
