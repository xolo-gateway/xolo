//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
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
	res, sent := messagesWithCachedSystem(t, modelClaude, system, "Bonjour.")

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
	if sent.Model != realModelClaude {
		t.Errorf("upstream model = %q, want %q", sent.Model, realModelClaude)
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

// The cache reads reported by the Messages upstream must land in the usage
// record, and be billed at the cached-prompt tariff.
func TestCacheControl_CachedTokensAreRecorded(t *testing.T) {
	start := time.Now().Add(-time.Second)
	messagesWithCachedSystem(t, modelClaude, "Tu es un assistant e2e.", "Compte jusqu'à trois.")

	record := waitForUsage(t, modelClaudeID, start)

	wantPrompt := fakeMessagesInputTokens + fakeMessagesCacheReadTokens
	if record.PromptTokens() != wantPrompt {
		t.Errorf("prompt tokens = %d, want %d (input + cache reads)", record.PromptTokens(), wantPrompt)
	}
	if record.CachedTokens() != fakeMessagesCacheReadTokens {
		t.Errorf("cached tokens = %d, want %d", record.CachedTokens(), fakeMessagesCacheReadTokens)
	}
	if record.CompletionTokens() != fakeMessagesOutputTokens {
		t.Errorf("completion tokens = %d, want %d", record.CompletionTokens(), fakeMessagesOutputTokens)
	}

	// Tariffs of the e2e Claude model (see createAnthropicProvider), applied
	// the way the usage tracker does: non-cached prompt at the full rate,
	// cached prompt at the cached rate, completion at its own rate.
	var (
		promptRate     int64 = 100
		cachedRate     int64 = 10
		completionRate int64 = 500
	)
	wantCost := int64(fakeMessagesInputTokens)*promptRate/1000 +
		int64(fakeMessagesCacheReadTokens)*cachedRate/1000 +
		int64(fakeMessagesOutputTokens)*completionRate/1000
	if record.ProviderCost() != wantCost {
		t.Errorf("provider cost = %d, want %d (cached tokens billed at the cached rate)", record.ProviderCost(), wantCost)
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
		if len(records) > 0 {
			return records[len(records)-1]
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no usage record for model %s within 10s", modelID)
	return nil
}
