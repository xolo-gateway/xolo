package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// fakeHost records the completion request and answers a canned content.
type fakeHost struct {
	pluginsdk.HostClient
	last    *proto.HostChatCompletionRequest
	content string
	err     error
}

func (f *fakeHost) ChatCompletion(_ context.Context, req *proto.HostChatCompletionRequest) (*proto.HostChatCompletionResponse, error) {
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	return &proto.HostChatCompletionResponse{Content: f.content}, nil
}

func run(t *testing.T, host *fakeHost, config, inputs, text string) map[string]any {
	t.Helper()
	p := &Plugin{}
	if host != nil {
		p.SetHostClient(host)
	}
	msgs, _ := json.Marshal([]map[string]string{
		{"role": "user", "content": "Bonjour"},
		{"role": "assistant", "content": "Bonjour, que puis-je faire ?"},
		{"role": "user", "content": text},
	})
	out, err := p.PreRequest(context.Background(), &proto.PreRequestInput{
		Ctx:          &proto.RequestContext{OrgId: "org1", UserId: "u1", ConfigJson: config},
		MessagesJson: string(msgs),
		InputsJson:   inputs,
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out.OutputsJson), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestClassify_JSONAnswer(t *testing.T) {
	host := &fakeHost{content: `{"category": "code", "confidence": 0.92, "reason": "asks for a Go function"}`}
	m := run(t, host, `{"model":"org/small"}`, "", "Écris une fonction Go qui inverse une slice.")
	if m["category"] != "code" || m["confidence"] != 0.92 || m["error"] != "" {
		t.Errorf("unexpected verdict: %v", m)
	}
	if host.last.Model != "org/small" || host.last.OrgId != "org1" || !host.last.JsonResponse {
		t.Errorf("unexpected request: %+v", host.last)
	}
	if len(host.last.Messages) != 2 || host.last.Messages[1].Role != "user" {
		t.Errorf("expected a system and a user message, got %+v", host.last.Messages)
	}
}

func TestClassify_ModelNamePortWins(t *testing.T) {
	host := &fakeHost{content: `{"category":"math","confidence":0.8}`}
	run(t, host, `{"model":"org/config-model"}`, `{"model_name":"org/port-model"}`, "Calcule 2+2")
	if host.last.Model != "org/port-model" {
		t.Errorf("the connected port must override the configured model, got %s", host.last.Model)
	}
}

func TestClassify_ProseAnswerFallsBackToNameSpotting(t *testing.T) {
	host := &fakeHost{content: "I think this is clearly a *math* question."}
	m := run(t, host, `{"model":"m"}`, "", "Résous x² = 4")
	if m["category"] != "math" || m["confidence"] != 0.5 {
		t.Errorf("unexpected verdict: %v", m)
	}
}

func TestClassify_UnknownCategoryYieldsFallback(t *testing.T) {
	host := &fakeHost{content: `{"category":"banana","confidence":1}`}
	m := run(t, host, `{"model":"m","fallback_category":"other"}`, "", "Hello")
	if m["category"] != "other" || m["error"] == "" {
		t.Errorf("expected fallback with an error, got %v", m)
	}
}

func TestClassify_HostErrors(t *testing.T) {
	m := run(t, nil, `{"model":"m"}`, "", "Hello")
	if m["category"] != "unknown" || m["error"] != "host client not connected" {
		t.Errorf("no host: %v", m)
	}
	m = run(t, &fakeHost{err: errors.New("boom")}, `{"model":"m"}`, "", "Hello")
	if m["category"] != "unknown" || m["error"] == "" {
		t.Errorf("call error: %v", m)
	}
	m = run(t, &fakeHost{content: "{}"}, ``, "", "Hello")
	if m["error"] == "" {
		t.Errorf("no model configured must be reported: %v", m)
	}
}

func TestClassify_CustomCategoriesAndHistory(t *testing.T) {
	host := &fakeHost{content: `{"category":"RH","confidence":0.7}`}
	cfg := `{"model":"m","include_history":true,"categories":[{"name":"RH","description":"ressources humaines"},{"name":"IT","description":"informatique"}]}`
	m := run(t, host, cfg, "", "Combien de jours de congés me reste-t-il ?")
	if m["category"] != "RH" {
		t.Errorf("unexpected verdict: %v", m)
	}
	sys := host.last.Messages[0].Content
	if !contains(sys, "- RH: ressources humaines") || !contains(sys, "- IT: informatique") {
		t.Errorf("categories must be listed in the system prompt: %s", sys)
	}
	if !contains(host.last.Messages[1].Content, "Earlier in the conversation") {
		t.Errorf("history excerpt expected: %s", host.last.Messages[1].Content)
	}
}

func TestParseVerdict_FencedJSON(t *testing.T) {
	v, ok := parseVerdict("```json\n{\"category\": \"Code\", \"confidence\": 3}\n```", []Category{{Name: "code"}})
	if !ok || v.Category != "code" || v.Confidence != 1 {
		t.Errorf("expected code/1 (clamped, case-insensitive), got %+v %v", v, ok)
	}
}

// Regression: when the model rambles and mentions more than one category, the
// most-mentioned one must win. The previous substring loop always returned the
// first declared category, which made the classifier look broken whenever the
// model answer had a preamble or an internal monologue.
func TestParseVerdict_MultiCategoryTextPicksMostMentioned(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}, {Name: "autre"}}
	v, ok := parseVerdict("It could be doc or doc, possibly doc, but definitely not really code.", cats)
	if !ok || v.Category != "doc" {
		t.Errorf("expected doc (mentioned 3x), got %+v ok=%v", v, ok)
	}
}

// Regression: ties are broken by declaration order so the verdict stays
// deterministic across runs.
func TestParseVerdict_TiesBreakByDeclarationOrder(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}}
	v, ok := parseVerdict("Could be code or doc.", cats)
	if !ok || v.Category != "code" {
		t.Errorf("expected code on tie (declared first), got %+v", v)
	}
}

// Regression: a stray prose prefix around the JSON must not swallow the
// object. The previous `(?s)\{.*\}` regex was greedy and would catch the first
// `{` to the very last `}` of the content, leaving json.Unmarshal with junk.
func TestParseVerdict_ProseAroundJSONIsIgnored(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}}
	v, ok := parseVerdict("Sure thing! Here you go:\n{\"category\": \"doc\", \"confidence\": 0.7}", cats)
	if !ok || v.Category != "doc" || v.Confidence != 0.7 {
		t.Errorf("expected doc/0.7, got %+v ok=%v", v, ok)
	}
}

// Regression: a fenced JSON is preferred over a bare object further down.
func TestParseVerdict_FencedBeforeBare(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}}
	content := "noise\n```json\n{\"category\": \"doc\", \"confidence\": 0.9}\n```\n{\"category\": \"code\"}"
	v, ok := parseVerdict(content, cats)
	if !ok || v.Category != "doc" {
		t.Errorf("expected the fenced doc verdict, got %+v", v)
	}
}

// Regression: substring matching must respect word boundaries so "code" does
// not bleed into "encoder", "decode" or "hardcoded".
func TestParseVerdict_WordBoundaryExcludesSubstrings(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}}
	// "code" appears only as a substring inside "encoder" / "hardcoded".
	v, ok := parseVerdict("Please rewrite the encoder and review the hardcoded values in this PR.", cats)
	if ok {
		t.Errorf("expected no match, got %+v (category=%q)", v, v.Category)
	}
	// A single, well-bounded mention of "code" still wins.
	v, ok = parseVerdict("The encoder is fine. The code could use a refactor.", cats)
	if !ok || v.Category != "code" {
		t.Errorf("expected code on a single word-boundary mention, got %+v", v)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
