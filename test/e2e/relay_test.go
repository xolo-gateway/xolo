//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// safeguards is the auto mode classifier request Claude Code adds to its
// requests, trimmed: the relay must hand it to the upstream as is.
const safeguards = `[{"type":"dangerous_tool_use","classifier_context":{"v":1,"permission_mode":"auto"}}]`

// streamMessages posts a streamed Messages request the way Claude Code does
// and returns the raw SSE body.
func streamMessages(t *testing.T, modelName, userText string) string {
	t.Helper()

	body := `{"model":` + string(mustJSON(modelName)) + `,"max_tokens":256,"stream":true,` +
		`"messages":[{"role":"user","content":` + string(mustJSON(userText)) + `}],` +
		`"safeguards":` + safeguards + `}`
	req, err := http.NewRequest(http.MethodPost, env.baseURL+"/api/v1/messages", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenAlice)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Beta", "dangerous-tool-use-2026-09-03")
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}
	return string(raw)
}

// streamedText is the text of the text_delta events of an SSE body.
func streamedText(t *testing.T, sse string) string {
	t.Helper()
	var text strings.Builder
	for _, line := range strings.Split(sse, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var event struct {
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("bad event %q: %v", data, err)
		}
		if event.Delta.Type == "text_delta" {
			text.WriteString(event.Delta.Text)
		}
	}
	return text.String()
}

// A streamed Messages request to an anthropic provider is relayed: the
// classifier review request reaches the upstream, its verdict comes back, and
// the gateway still authenticates, routes and bills the call.
func TestMessagesRelay_KeepsClassifierReview(t *testing.T) {
	before := len(env.provider.Requests())
	start := time.Now()

	sse := streamMessages(t, modelClaudeDirect, "Relais direct.")

	sent := env.provider.RequestsSince(before)
	if len(sent) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(sent))
	}
	upstream := sent[0]
	if !strings.Contains(upstream.Raw, `"safeguards":`+safeguards) {
		t.Errorf("safeguards not relayed as is: %s", upstream.Raw)
	}
	if upstream.Model != realModelClaudeDirect {
		t.Errorf("upstream model = %q, want %q", upstream.Model, realModelClaudeDirect)
	}
	if upstream.Header.Get("Anthropic-Beta") != "dangerous-tool-use-2026-09-03" {
		t.Errorf("anthropic-beta = %q", upstream.Header.Get("Anthropic-Beta"))
	}
	if upstream.Header.Get("Authorization") != "" || strings.Contains(upstream.Header.Get("X-Api-Key"), tokenAlice) {
		t.Errorf("the gateway token leaked upstream: %v", upstream.Header)
	}
	if upstream.Header.Get("X-Api-Key") == "" {
		t.Error("the provider key must be sent upstream")
	}

	if !strings.Contains(sse, `"safeguard_results":[{"status":{"tool_uses":{},"type":"available"},"type":"dangerous_tool_use"}]`) {
		t.Errorf("the verdict did not reach the client:\n%s", sse)
	}
	if got := streamedText(t, sse); got != "Bien reçu : Relais direct." {
		t.Errorf("streamed text = %q", got)
	}

	record := waitForUsage(t, modelClaudeDirectID, start)
	if want := fakeMessagesInputTokens + fakeMessagesCacheReadTokens + fakeMessagesCacheCreationTokens; record.PromptTokens() != want {
		t.Errorf("prompt tokens = %d, want %d", record.PromptTokens(), want)
	}
	if record.CompletionTokens() != fakeMessagesOutputTokens {
		t.Errorf("completion tokens = %d, want %d", record.CompletionTokens(), fakeMessagesOutputTokens)
	}
}

// Through the pseudonymizer, the relayed request carries placeholders only,
// the client gets the names back, and the verdict is left alone.
func TestMessagesRelay_PseudonymizerStillApplies(t *testing.T) {
	before := len(env.provider.Requests())

	sse := streamMessages(t, modelClaude, "Bonjour, je m'appelle Jean Dupont.")

	sent := env.provider.RequestsSince(before)
	if len(sent) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(sent))
	}
	if strings.Contains(sent[0].Raw, "Jean Dupont") {
		t.Errorf("the relayed request leaked the name: %s", sent[0].Raw)
	}
	if !strings.Contains(sent[0].Raw, `"safeguards":`+safeguards) {
		t.Errorf("safeguards lost behind the pseudonymizer: %s", sent[0].Raw)
	}

	if got := streamedText(t, sse); !strings.Contains(got, "Jean Dupont") || strings.Contains(got, "PERSON_") {
		t.Errorf("streamed text = %q, want the name restored", got)
	}
	if !strings.Contains(sse, `"safeguard_results"`) {
		t.Errorf("the verdict did not reach the client:\n%s", sse)
	}
}
