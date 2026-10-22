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

// postDecision sends body to the decision route as token.
func postDecision(t *testing.T, token string, body map[string]any) (int, string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, env.baseURL+"/api/v1/decisions", bytes.NewReader(mustJSON(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("decision request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func decisionBody(modelName string) map[string]any {
	return map[string]any{
		"model": modelName,
		"state": map[string]any{"ticket": "Mes virements échouent depuis trois jours."},
		"questions": map[string]any{
			"is_urgent": map[string]any{"type": "noul", "instructions": "Ce ticket est-il urgent ?"},
		},
	}
}

func TestDecisions_ServedAndBilled(t *testing.T) {
	before := len(env.provider.Requests())
	start := time.Now()

	status, body := postDecision(t, tokenAlice, decisionBody(modelJev))
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}

	var resp struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type string   `json:"type"`
			Noul *float64 `json:"noul"`
		} `json:"answers"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if a := resp.Answers["is_urgent"]; a.Type != "noul" || a.Noul == nil || *a.Noul != 0.75 {
		t.Errorf("is_urgent = %+v", a)
	}
	if resp.Usage.InputTokens != 200 || resp.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v", resp.Usage)
	}

	upstream := env.provider.RequestsSince(before)
	if len(upstream) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(upstream))
	}
	if upstream[0].Model != realModelJev {
		t.Errorf("upstream model = %q, want the real model %q", upstream[0].Model, realModelJev)
	}
	if !strings.Contains(upstream[0].Raw, "virements") {
		t.Errorf("the state must reach the provider: %s", upstream[0].Raw)
	}
	if strings.Contains(upstream[0].Header.Get("Authorization"), tokenAlice) {
		t.Errorf("the client's Xolo token must never reach the provider")
	}

	// 200 input tokens at 100 microcents per 1K, 2 output tokens at 400.
	record := waitForUsage(t, modelJevID, start)
	if record.PromptTokens() != 200 || record.CompletionTokens() != 2 {
		t.Errorf("tokens = %d/%d, want 200/2", record.PromptTokens(), record.CompletionTokens())
	}
	if want := int64(200*100/1000 + 2*400/1000); record.ProviderCost() != want {
		t.Errorf("provider cost = %d, want %d", record.ProviderCost(), want)
	}
}

func TestDecisions_EachModelToItsRoute(t *testing.T) {
	before := len(env.provider.Requests())

	if status, body := postDecision(t, tokenAlice, decisionBody(modelFast)); status != http.StatusBadRequest || !strings.Contains(body, "is a completion model") {
		t.Errorf("a decision on a chat model: status = %d, body = %s", status, body)
	}
	if res := chat(t, tokenAlice, modelJev, "Bonjour"); res.Status != http.StatusBadRequest || !strings.Contains(res.Body, "/decisions") {
		t.Errorf("a chat on a decision model: status = %d, body = %s", res.Status, res.Body)
	}
	if calls := len(env.provider.RequestsSince(before)); calls != 0 {
		t.Errorf("a refused request must not reach the provider, got %d calls", calls)
	}
}
