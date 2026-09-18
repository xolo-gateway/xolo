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

// The blocks genai returns come in document order and the first one carrying a
// category wins. A fenced block has no privilege of its own — the fence is
// prose around the object — so the invariant is checked both ways round.
func TestParseVerdict_FirstJSONObjectWins(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}}
	fencedFirst := "noise\n```json\n{\"category\": \"doc\"}\n```\n{\"category\": \"code\"}"
	if v, ok := parseVerdict(fencedFirst, cats); !ok || v.Category != "doc" {
		t.Errorf("expected doc, the first object, got %+v ok=%v", v, ok)
	}
	bareFirst := "{\"category\": \"code\"}\n```json\n{\"category\": \"doc\"}\n```"
	if v, ok := parseVerdict(bareFirst, cats); !ok || v.Category != "code" {
		t.Errorf("expected code, the first object, got %+v ok=%v", v, ok)
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

// A brace inside a JSON string value must not break the extraction. This
// locks the behaviour rather than fixing a bug: the greedy regexp this branch
// started from already decoded it. The non-greedy regexp tried in between did
// not, which is how the case surfaced.
func TestParseVerdict_BracesInsideJSONString(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}, {Name: "autre"}}
	content := `{"category":"doc","confidence":0.95,"reason":"asks to document the code, not to write code like func(){}"}`
	v, ok := parseVerdict(content, cats)
	if !ok || v.Category != "doc" || v.Confidence != 0.95 {
		t.Errorf("expected doc/0.95 from the JSON verdict, got %+v ok=%v", v, ok)
	}
}

// A nested object is valid JSON and must decode rather than fall through to
// the prose count. Behaviour lock, same story as the test above.
func TestParseVerdict_NestedObjectIsParsed(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}}
	content := `{"category":"code","confidence":0.8,"meta":{"tokens":12}}`
	v, ok := parseVerdict(content, cats)
	if !ok || v.Category != "code" || v.Confidence != 0.8 {
		t.Errorf("expected code/0.8, got %+v ok=%v", v, ok)
	}
}

// Regression: word boundaries are decoded as runes. With a byte test, the
// continuation bytes of "é" passed for a boundary and "décode" counted as a
// mention of "code" — the very false positive this path exists to avoid.
func TestParseVerdict_AccentedWordIsNotAMatch(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}}
	if n := countWordMatches("il faut décode", "code"); n != 0 {
		t.Errorf("expected 0 mention inside \"décode\", got %d", n)
	}
	if n := countWordMatches("une école", "cole"); n != 0 {
		t.Errorf("expected 0 mention inside \"école\", got %d", n)
	}
	// Decomposed form: "de" + U+0301 + "code". A combining mark is part of the
	// word, so it is a boundary no more than the letter it decorates.
	if n := countWordMatches("il faut de\u0301code", "code"); n != 0 {
		t.Errorf("expected 0 mention inside the decomposed \"décode\", got %d", n)
	}
	v, ok := parseVerdict("Il faut décode le flux, puis relire la doc.", cats)
	if !ok || v.Category != "doc" {
		t.Errorf("expected doc (code only appears inside \"décode\"), got %+v ok=%v", v, ok)
	}
}

// Delegating to genai buys tolerance a hand-rolled extractor did not have:
// the trailing commas and single quotes small models emit go through
// json-repair instead of falling back to the prose count. Confidence is only
// forwarded to the output port, never compared to a threshold, so the float32
// round-trip json-repair performs on a repaired payload is harmless.
func TestParseVerdict_SloppyJSONIsRepaired(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}}
	for _, content := range []string{
		`{"category":"doc","confidence":0.7,}`,
		`{'category': 'doc', 'confidence': 0.7}`,
	} {
		v, ok := parseVerdict(content, cats)
		if !ok || v.Category != "doc" {
			t.Errorf("expected doc from %q, got %+v ok=%v", content, v, ok)
		}
		if v.Confidence < 0.69 || v.Confidence > 0.71 {
			t.Errorf("expected confidence near 0.7 from %q, got %v", content, v.Confidence)
		}
	}
}

// Regression: a scratchpad object before the verdict used to merge with it.
// Both the greedy regexp on main and the one genai shipped until v0.42.0 ran
// from the first `{` of the answer to its last `}`, so json-repair resolved the
// merged block to the stray object, the category came out empty, and the prose
// count answered "code" against a JSON verdict that said "doc".
func TestParseVerdict_StrayObjectBeforeVerdict(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}, {Name: "autre"}}
	for _, content := range []string{
		`code code code {"a":1} {"category":"doc","confidence":0.9}`,
		`code code code {} {"category":"doc","confidence":0.9}`,
		`The user wrote { and } in their code code code. Verdict: {"category":"doc","confidence":0.9}`,
		`code { code {"category":"doc","confidence":0.9}`,
	} {
		v, ok := parseVerdict(content, cats)
		if !ok || v.Category != "doc" {
			t.Errorf("expected doc from %q, got %+v ok=%v", content, v, ok)
		}
	}
}

// A scratchpad block carrying a category of its own must not shadow the real
// verdict either. Filtering on a non-empty `category` field was not enough:
// the draft won, matchCategory rejected it, and the decision fell to the prose
// count — which answered "doc" here too, but with its flat 0.5 instead of the
// confidence the model gave. The assertion is on the confidence for that
// reason.
func TestParseVerdict_DraftWithUnknownCategoryIsSkipped(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}}
	v, ok := parseVerdict(`{"category":"unsure"} then {"category":"doc","confidence":0.9}`, cats)
	if !ok || v.Category != "doc" {
		t.Fatalf("expected doc, got %+v ok=%v", v, ok)
	}
	if v.Confidence != 0.9 {
		t.Errorf("expected the confidence carried by the real verdict, got %v", v.Confidence)
	}
}

// A plural on the JSON answer is a category the model picked, not one it made
// up. Before the word-boundary rule, the substring match let it through; the
// tolerance lives in matchCategory so it stays off the prose path.
func TestParseVerdict_PluralJSONCategoryMatches(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}}
	v, ok := parseVerdict(`{"category":"docs","confidence":0.9}`, cats)
	if !ok || v.Category != "doc" || v.Confidence != 0.9 {
		t.Errorf("expected doc/0.9, got %+v ok=%v", v, ok)
	}
	// A category the model invented still falls through.
	if v, ok := parseVerdict(`{"category":"banana"}`, cats); ok {
		t.Errorf("expected no match for an invented category, got %+v", v)
	}
}

// A payload cut short by max_tokens is closed by json-repair instead of being
// dropped. Neither regexp could match it: both needed a closing brace.
func TestParseVerdict_TruncatedJSONIsRecovered(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}}
	v, ok := parseVerdict(`{"category":"doc","confidence":0.9`, cats)
	if !ok || v.Category != "doc" {
		t.Errorf("expected doc from a truncated payload, got %+v ok=%v", v, ok)
	}
	// The confidence proves the verdict came from the payload: the prose count
	// would also answer "doc" here, but with its flat 0.5.
	if v.Confidence < 0.89 || v.Confidence > 0.91 {
		t.Errorf("expected the confidence carried by the payload, got %v", v.Confidence)
	}
}

// The word-boundary rule drops inflected forms. This is the accepted cost of
// excluding "encoder" and "hardcoded"; the test exists so the trade-off is not
// changed by accident.
func TestParseVerdict_InflectedFormsAreNotMatches(t *testing.T) {
	for _, tc := range []struct{ haystack, needle string }{
		{"i need it coded", "code"},
		{"it is a maths problem", "math"},
		{"check the docs", "doc"},
	} {
		if n := countWordMatches(tc.haystack, tc.needle); n != 0 {
			t.Errorf("expected %q not to count as a mention of %q, got %d", tc.haystack, tc.needle, n)
		}
	}
	// Punctuation is a boundary, so these do count.
	for _, tc := range []struct{ haystack, needle string }{
		{"a code-based answer", "code"},
		{"the code's author", "code"},
	} {
		if n := countWordMatches(tc.haystack, tc.needle); n != 1 {
			t.Errorf("expected %q to count once as %q, got %d", tc.haystack, tc.needle, n)
		}
	}
}

// A JSON verdict naming a category that is not configured falls through to the
// prose count, which runs over the raw answer — the JSON text included. With
// no configured category named anywhere, parseVerdict reports no match and the
// caller applies fallback_category.
func TestParseVerdict_UnknownJSONCategoryFallsThrough(t *testing.T) {
	cats := []Category{{Name: "code"}, {Name: "doc"}}
	if v, ok := parseVerdict(`{"category":"banana","confidence":0.9}`, cats); ok {
		t.Errorf("expected no match for an unconfigured category, got %+v", v)
	}
	// When the answer does name a configured category, that one wins.
	v, ok := parseVerdict(`{"category":"banana","reason":"really about the doc"}`, cats)
	if !ok || v.Category != "doc" {
		t.Errorf("expected doc from the prose count, got %+v ok=%v", v, ok)
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
