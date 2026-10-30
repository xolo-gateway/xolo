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

// messagesRequest drives the Anthropic Messages route, where the system prompt
// is a TOP-LEVEL field rather than a message — which is the whole point of this
// file. `chatMessages` cannot be reused: it posts to the OpenAI route, where a
// system prompt is just another entry in `messages`.
func messagesRequest(t *testing.T, token, modelName, system string, messages []map[string]any) chatResult {
	t.Helper()

	body := map[string]any{
		"model":      modelName,
		"max_tokens": 256,
		"messages":   messages,
	}
	if system != "" {
		body["system"] = system
	}
	return postMessages(t, token, body)
}

// postMessages sends an arbitrary body to the Anthropic Messages route, for
// the tests that need a system prompt as an array of blocks.
func postMessages(t *testing.T, token string, body map[string]any) chatResult {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, env.baseURL+"/api/v1/messages", bytes.NewReader(mustJSON(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("messages request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	res := chatResult{Status: resp.StatusCode, Body: string(raw)}
	var parsed struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &parsed) == nil && len(parsed.Content) > 0 {
		res.Content = parsed.Content[0].Text
	}
	return res
}

func TestMessagesRoute_ClientSystemPromptSurvivesARewritingNode(t *testing.T) {
	before := len(env.provider.Requests())

	const systemPrompt = "Tu es l'assistant du client, et tu réponds toujours en français."
	res := messagesRequest(t, tokenAlice, modelTagStrategy, systemPrompt, []map[string]any{
		{"role": "user", "content": "Bonjour, je m'appelle Jean Dupont."},
	})
	if res.Status != 200 {
		t.Fatalf("status = %d, body = %s", res.Status, res.Body)
	}

	upstream := env.provider.RequestsSince(before)
	if len(upstream) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(upstream))
	}
	sent := upstream[0]

	// The node did rewrite: without this the test could pass on a request the
	// plugin never touched, and prove nothing about the rebuild.
	if strings.Contains(sent.Raw, "Jean Dupont") {
		t.Fatalf("the pseudonymizer did not rewrite this request: %s", sent.Raw)
	}

	if !strings.Contains(sent.Raw, systemPrompt) {
		t.Errorf("the client's system prompt never reached the provider.\nsent: %s", sent.Raw)
	}

	var carried bool
	for _, m := range sent.Messages {
		if m.Role != "system" {
			continue
		}
		if text, ok := m.Content.(string); ok && strings.Contains(text, systemPrompt) {
			carried = true
		}
	}
	if !carried {
		t.Errorf("the system prompt did not reach the provider as a system message: %+v", sent.Messages)
	}
}

// The control: same route, same shape, a model with no node rewriting anything.
func TestMessagesRoute_ClientSystemPromptWithoutARewritingNode(t *testing.T) {
	before := len(env.provider.Requests())

	const systemPrompt = "Tu es l'assistant du client, et tu réponds toujours en français."
	res := messagesRequest(t, tokenAlice, modelFast, systemPrompt, []map[string]any{
		{"role": "user", "content": "Bonjour."},
	})
	if res.Status != 200 {
		t.Fatalf("status = %d, body = %s", res.Status, res.Body)
	}

	upstream := env.provider.RequestsSince(before)
	if len(upstream) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(upstream))
	}
	if !strings.Contains(upstream[0].Raw, systemPrompt) {
		t.Errorf("the system prompt is lost even with no rewriting node.\nsent: %s", upstream[0].Raw)
	}
}

// postStreamedMessages posts a streamed request to the Anthropic Messages route and
// returns the status and the raw server-sent events.
func postStreamedMessages(t *testing.T, token string, body map[string]any) (int, string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, env.baseURL+"/api/v1/messages", bytes.NewReader(mustJSON(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("messages request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// toolUseInput reassembles the input of the tool_use blocks of a Messages
// event stream from their input_json_delta fragments.
func toolUseInput(t *testing.T, events string) string {
	t.Helper()

	toolUse := map[int]bool{}
	var input strings.Builder
	for _, line := range strings.Split(events, "\n") {
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		var event struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &event) != nil {
			continue
		}
		switch {
		case event.Type == "content_block_start" && event.ContentBlock.Type == "tool_use":
			toolUse[event.Index] = true
		case event.Delta.Type == "input_json_delta" && toolUse[event.Index]:
			input.WriteString(event.Delta.PartialJSON)
		}
	}
	if len(toolUse) == 0 {
		t.Fatalf("no tool_use block in the stream:\n%s", events)
	}
	return input.String()
}
