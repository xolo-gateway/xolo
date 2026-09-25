//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	gormadapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// cachedSystemPrompt is a system prompt sent as an array of blocks, the last
// one carrying a cache breakpoint, the way Anthropic SDK clients do.
func cachedSystemPrompt(text string) []map[string]any {
	return []map[string]any{{
		"type":          "text",
		"text":          text,
		"cache_control": map[string]any{"type": "ephemeral"},
	}}
}

// messagesWithCachedSystem posts a Messages request with a cached system
// prompt to the given model and returns the upstream request it produced.
func messagesWithCachedSystem(t *testing.T, modelName, system, userText string) (chatResult, chatRequest) {
	t.Helper()
	before := len(env.provider.Requests())

	res := postMessages(t, tokenAlice, map[string]any{
		"model":      modelName,
		"max_tokens": 256,
		"system":     cachedSystemPrompt(system),
		"messages":   []map[string]any{{"role": "user", "content": userText}},
	})
	if res.Status != 200 {
		t.Fatalf("status = %d, body = %s", res.Status, res.Body)
	}

	upstream := env.provider.RequestsSince(before)
	if len(upstream) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(upstream))
	}
	return res, upstream[0]
}

// systemCacheControl returns the cache_control of the first system block of
// an upstream Messages request, or nil when the system field is a plain
// string or carries no breakpoint.
func systemCacheControl(t *testing.T, raw string) map[string]any {
	t.Helper()
	var body struct {
		System []struct {
			Text         string         `json:"text"`
			CacheControl map[string]any `json:"cache_control"`
		} `json:"system"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil || len(body.System) == 0 {
		return nil
	}
	return body.System[0].CacheControl
}

func TestCacheControl_ReachesAnthropicUpstream(t *testing.T) {
	const system = "Tu es un assistant e2e qui répond en une phrase."
	// The direct model: no middleware in the way, and the embeddings
	// capability ticked, which must not break chat completions on a
	// provider type without an embeddings client.
	res, sent := messagesWithCachedSystem(t, modelClaudeDirect, system, "Bonjour.")

	if !strings.Contains(res.Content, "Bonjour.") {
		t.Errorf("the fake's echo did not come back: %q", res.Content)
	}

	cc := systemCacheControl(t, sent.Raw)
	if cc == nil || cc["type"] != "ephemeral" {
		t.Fatalf("the cache breakpoint did not reach the Messages upstream.\nsent: %s", sent.Raw)
	}
	if strings.Contains(sent.Raw, `"role":"system"`) {
		t.Errorf("the system prompt must travel in the top-level system field, not as a message.\nsent: %s", sent.Raw)
	}
	if sent.Model != realModelClaudeDirect {
		t.Errorf("upstream model = %q, want %q", sent.Model, realModelClaudeDirect)
	}
	var body struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal([]byte(sent.Raw), &body); err != nil {
		t.Fatalf("could not parse the upstream request: %v", err)
	}
	if body.MaxTokens != 256 {
		t.Errorf("the client's max_tokens must reach the upstream, got %d", body.MaxTokens)
	}
}

// A breakpoint on a message content part, not only on the system prompt,
// must reach the upstream. genai's message model carries one text per
// message, so the parts are merged into a single block and the breakpoint
// covers the whole message: the cached prefix still ends where the client
// asked, at the end of that message.
func TestCacheControl_OnMessageContentPart(t *testing.T) {
	before := len(env.provider.Requests())
	res := postMessages(t, tokenAlice, map[string]any{
		"model":      modelClaudeDirect,
		"max_tokens": 256,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": "Voici un long contexte de référence."},
				{"type": "text", "text": "Résume-le.", "cache_control": map[string]any{"type": "ephemeral"}},
			},
		}},
	})
	if res.Status != 200 {
		t.Fatalf("status = %d, body = %s", res.Status, res.Body)
	}
	upstream := env.provider.RequestsSince(before)
	if len(upstream) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(upstream))
	}
	var body struct {
		Messages []struct {
			Content []struct {
				Text         string         `json:"text"`
				CacheControl map[string]any `json:"cache_control"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(upstream[0].Raw), &body); err != nil || len(body.Messages) == 0 {
		t.Fatalf("could not parse the upstream request: %v\n%s", err, upstream[0].Raw)
	}
	parts := body.Messages[0].Content
	if len(parts) == 0 {
		t.Fatalf("no content part reached the upstream: %s", upstream[0].Raw)
	}
	last := parts[len(parts)-1]
	if !strings.Contains(last.Text, "Résume-le.") || !strings.Contains(upstream[0].Raw, "long contexte") {
		t.Errorf("both texts must reach the upstream: %s", upstream[0].Raw)
	}
	if cc := last.CacheControl; cc == nil || cc["type"] != "ephemeral" {
		t.Errorf("the breakpoint must sit on the last block of the message: %s", upstream[0].Raw)
	}
}

// Without a max_tokens from the client, the model's output window is what
// reaches the upstream, not the provider's 4096 default.
func TestCacheControl_OutputWindowIsTheDefaultMaxTokens(t *testing.T) {
	before := len(env.provider.Requests())
	res := chat(t, tokenAlice, modelClaudeDirect, "Bonjour.")
	if res.Status != 200 {
		t.Fatalf("status = %d, body = %s", res.Status, res.Body)
	}
	upstream := env.provider.RequestsSince(before)
	if len(upstream) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(upstream))
	}
	var body struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal([]byte(upstream[0].Raw), &body); err != nil {
		t.Fatal(err)
	}
	if body.MaxTokens != 8_192 {
		t.Errorf("max_tokens = %d, want the model's output window 8192", body.MaxTokens)
	}
}

// The control: the very same request to a provider of type openai must not
// grow a field the OpenAI schema does not know.
func TestCacheControl_NotEmittedOnOpenAIUpstream(t *testing.T) {
	_, sent := messagesWithCachedSystem(t, modelFast, "Tu es un assistant e2e.", "Bonjour.")

	if strings.Contains(sent.Raw, "cache_control") {
		t.Errorf("cache_control leaked into an OpenAI-compatible request.\nsent: %s", sent.Raw)
	}
}

// A middleware rewriting the request (pseudonymizer) must not lose the cache
// breakpoint when it rebuilds the messages.
func TestCacheControl_SurvivesARewritingNode(t *testing.T) {
	const system = "Tu es un assistant e2e discret."
	res, sent := messagesWithCachedSystem(t, modelClaude, system, "Bonjour, je m'appelle Jean Dupont.")

	if strings.Contains(sent.Raw, "Jean Dupont") {
		t.Fatalf("the pseudonymizer did not rewrite this request: %s", sent.Raw)
	}
	if cc := systemCacheControl(t, sent.Raw); cc == nil || cc["type"] != "ephemeral" {
		t.Errorf("the cache breakpoint was lost across the rewriting node.\nsent: %s", sent.Raw)
	}
	if !strings.Contains(res.Content, "Jean Dupont") {
		t.Errorf("the client must get the original name back: %q", res.Content)
	}
}

// A conversation that brings a new entity on each turn must keep the prefix it
// sent upstream on the previous turn, or the prompt cache never reaches past
// the client's system prompt and every turn is billed at full price (#85).
func TestCacheControl_PseudonymizedPrefixIsStableAcrossTurns(t *testing.T) {
	const system = "Tu es un assistant e2e discret."
	// A client resends the history as it received it: the assistant turns
	// carry the restored values, which the plugin pseudonymizes again.
	turns := [][]map[string]any{
		{
			{"role": "user", "content": "Bonjour, je m'appelle Jean Dupont."},
		},
		{
			{"role": "assistant", "content": "Bonjour Jean Dupont."},
			{"role": "user", "content": "Mon collègue Pierre Martin habite à Lyon."},
		},
		{
			{"role": "assistant", "content": "Noté : Pierre Martin, à Lyon."},
			{"role": "user", "content": "Écris un courriel à Jean Dupont et Pierre Martin."},
		},
	}
	// The fake provider echoes the last user message: each answer must come
	// back with the names of that message restored.
	restored := [][]string{
		{"Jean Dupont"},
		{"Pierre Martin"},
		{"Jean Dupont", "Pierre Martin"},
	}

	type upstreamBody struct {
		System   json.RawMessage   `json:"system"`
		Messages []json.RawMessage `json:"messages"`
	}

	var (
		history  []map[string]any
		previous *upstreamBody
	)
	for turn, messages := range turns {
		history = append(history, messages...)

		before := len(env.provider.Requests())
		res := postMessages(t, tokenAlice, map[string]any{
			"model":      modelClaude,
			"max_tokens": 256,
			"system":     cachedSystemPrompt(system),
			"messages":   history,
		})
		if res.Status != 200 {
			t.Fatalf("turn %d: status = %d, body = %s", turn+1, res.Status, res.Body)
		}
		upstream := env.provider.RequestsSince(before)
		if len(upstream) != 1 {
			t.Fatalf("turn %d: upstream calls = %d, want 1", turn+1, len(upstream))
		}
		for _, name := range []string{"Jean Dupont", "Pierre Martin"} {
			if strings.Contains(upstream[0].Raw, name) {
				t.Fatalf("turn %d: %q reached the upstream: %s", turn+1, name, upstream[0].Raw)
			}
		}
		for _, name := range restored[turn] {
			if !strings.Contains(res.Content, name) {
				t.Errorf("turn %d: %q was not restored in the answer: %q", turn+1, name, res.Content)
			}
		}
		if cc := systemCacheControl(t, upstream[0].Raw); cc == nil || cc["type"] != "ephemeral" {
			t.Errorf("turn %d: the cache breakpoint did not reach the upstream.\nsent: %s", turn+1, upstream[0].Raw)
		}

		var current upstreamBody
		if err := json.Unmarshal([]byte(upstream[0].Raw), &current); err != nil {
			t.Fatalf("turn %d: could not parse the upstream request: %v", turn+1, err)
		}
		if len(current.Messages) != len(history) {
			t.Fatalf("turn %d: upstream messages = %d, want %d", turn+1, len(current.Messages), len(history))
		}

		if previous != nil {
			if !bytes.Equal(previous.System, current.System) {
				t.Errorf("turn %d: the system prompt changed.\nbefore: %s\nnow:    %s", turn+1, previous.System, current.System)
			}
			for i := range previous.Messages {
				if !bytes.Equal(previous.Messages[i], current.Messages[i]) {
					t.Errorf("turn %d: message %d changed.\nbefore: %s\nnow:    %s", turn+1, i, previous.Messages[i], current.Messages[i])
				}
			}
		}
		previous = &current
	}
}

// The cache reads reported by the Messages upstream must land in the usage
// record, and be billed at the cached-prompt tariff.
func TestCacheControl_CachedTokensAreRecorded(t *testing.T) {
	start := time.Now()
	messagesWithCachedSystem(t, modelClaude, "Tu es un assistant e2e.", "Compte jusqu'à trois.")

	record := waitForUsage(t, modelClaudeID, start)

	wantPrompt := fakeMessagesInputTokens + fakeMessagesCacheReadTokens + fakeMessagesCacheCreationTokens
	if record.PromptTokens() != wantPrompt {
		t.Errorf("prompt tokens = %d, want %d (input + cache reads + cache writes)", record.PromptTokens(), wantPrompt)
	}
	if record.CachedTokens() != fakeMessagesCacheReadTokens {
		t.Errorf("cached tokens = %d, want %d", record.CachedTokens(), fakeMessagesCacheReadTokens)
	}
	if record.CompletionTokens() != fakeMessagesOutputTokens {
		t.Errorf("completion tokens = %d, want %d", record.CompletionTokens(), fakeMessagesOutputTokens)
	}

	// Tariffs of the e2e Claude model (see createAnthropicProvider), applied
	// the way the usage tracker does: non-cached prompt (input and cache
	// writes) at the full rate, cache reads at the cached rate, completion
	// at its own rate.
	var (
		promptRate     int64 = 100
		cachedRate     int64 = 10
		completionRate int64 = 500
	)
	wantCost := int64(fakeMessagesInputTokens+fakeMessagesCacheCreationTokens)*promptRate/1000 +
		int64(fakeMessagesCacheReadTokens)*cachedRate/1000 +
		int64(fakeMessagesOutputTokens)*completionRate/1000
	if record.ProviderCost() != wantCost {
		t.Errorf("provider cost = %d, want %d (cached tokens billed at the cached rate)", record.ProviderCost(), wantCost)
	}
}

// A model without a cached-prompt tariff bills cache reads at the full
// prompt rate, never for free.
func TestCacheControl_MissingCachedTariffFallsBackToFullRate(t *testing.T) {
	start := time.Now()
	messagesWithCachedSystem(t, modelClaudeDirect, "Tu es un assistant e2e.", "Compte jusqu'à deux.")

	record := waitForUsage(t, modelClaudeDirectID, start)

	var promptRate, completionRate int64 = 100, 500
	wantCost := int64(fakeMessagesInputTokens+fakeMessagesCacheReadTokens+fakeMessagesCacheCreationTokens)*promptRate/1000 +
		int64(fakeMessagesOutputTokens)*completionRate/1000
	if record.ProviderCost() != wantCost {
		t.Errorf("provider cost = %d, want %d (no cached tariff: full rate on every prompt token)", record.ProviderCost(), wantCost)
	}
}

// waitForUsage polls the usage store until a record for the model appears
// after start, the tracker writing asynchronously.
func waitForUsage(t *testing.T, modelID string, start time.Time) model.UsageRecord {
	t.Helper()
	db, err := openDB(env.dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()

	orgID := model.OrgID(orgAcme)
	id := model.LLMModelID(modelID)
	filter := port.UsageFilter{OrgID: &orgID, ModelID: &id, Since: &start}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		records, err := gormadapter.NewStore(db).QueryUsage(context.Background(), filter)
		if err != nil {
			t.Fatalf("query usage: %v", err)
		}
		// The tracker records usage in the PostResponse hook, before the
		// response reaches the client, so a previous scenario's record
		// always predates this one's start. A second record can only be
		// this request billed twice, which no retry should turn into a pass.
		if len(records) > 1 {
			t.Fatalf("expected a single usage record since %s for model %s, got %d", start.Format(time.RFC3339Nano), modelID, len(records))
		}
		if len(records) == 1 {
			return records[0]
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no usage record for model %s within 10s", modelID)
	return nil
}

// The OpenAI-format streaming route over an anthropic provider: the fake
// upstream flushes each SSE event, so the deltas cross the whole chain
// incrementally and must add up to the echoed answer.
func TestCacheControl_StreamingRoundTrip(t *testing.T) {
	before := len(env.provider.Requests())
	start := time.Now()

	payload := mustJSON(map[string]any{
		"model":    modelClaudeDirect,
		"stream":   true,
		"messages": []map[string]any{{"role": "user", "content": "Bonjour en streaming."}},
	})
	req, err := http.NewRequest(http.MethodPost, env.baseURL+"/api/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenAlice)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var content strings.Builder
	var chunks int
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatalf("bad chunk %q: %v", line, err)
		}
		chunks++
		for _, c := range chunk.Choices {
			content.WriteString(c.Delta.Content)
		}
	}
	if chunks == 0 {
		t.Fatal("no streamed chunk received")
	}
	if !strings.Contains(content.String(), "Bien reçu : Bonjour en streaming.") {
		t.Errorf("streamed content = %q", content.String())
	}
	if len(env.provider.RequestsSince(before)) != 1 {
		t.Errorf("upstream calls = %d, want 1", len(env.provider.RequestsSince(before)))
	}

	// The streaming path rebuilds its usage separately from the plain one:
	// the cache counters must come through it too.
	record := waitForUsage(t, modelClaudeDirectID, start)
	if record.CachedTokens() != fakeMessagesCacheReadTokens {
		t.Errorf("streamed cached tokens = %d, want %d", record.CachedTokens(), fakeMessagesCacheReadTokens)
	}
	wantPrompt := fakeMessagesInputTokens + fakeMessagesCacheReadTokens + fakeMessagesCacheCreationTokens
	if record.PromptTokens() != wantPrompt {
		t.Errorf("streamed prompt tokens = %d, want %d", record.PromptTokens(), wantPrompt)
	}
	if record.CompletionTokens() != fakeMessagesOutputTokens {
		t.Errorf("streamed completion tokens = %d, want %d", record.CompletionTokens(), fakeMessagesOutputTokens)
	}
}
