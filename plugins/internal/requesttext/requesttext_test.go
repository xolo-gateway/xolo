package requesttext

import "testing"

const sample = `[
  {"role":"system","content":"Tu es un assistant."},
  {"role":"user","content":[{"type":"text","text":"Décris cette image"},{"type":"image_url","image_url":{"url":"data:..."}}]},
  {"role":"assistant","content":null,"tool_calls":[{"function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},
  {"role":"tool","content":"résultat"}
]`

func TestPrompt_OnlySystemAndUser(t *testing.T) {
	got := Prompt(sample)
	if got != "Tu es un assistant. Décris cette image  " {
		t.Fatalf("unexpected prompt: %q", got)
	}
}

func TestContext_IncludesToolsAndArguments(t *testing.T) {
	got := Context(sample)
	for _, want := range []string{"Tu es un assistant.", "Décris cette image", `{"q":"x"}`, "résultat"} {
		if !contains(got, want) {
			t.Errorf("context %q lacks %q", got, want)
		}
	}
}

func TestHasImage(t *testing.T) {
	if !HasImage(sample) {
		t.Error("expected an image")
	}
	if HasImage(`[{"role":"user","content":"hello"}]`) {
		t.Error("expected no image")
	}
	if HasImage("not json") {
		t.Error("invalid JSON must not report an image")
	}
}

func TestEstimateTokens(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Error("empty text has no tokens")
	}
	if got := EstimateTokens("abcdefgh"); got != 2 {
		t.Errorf("expected 2 tokens, got %d", got)
	}
	if got := EstimateTokens("abcde"); got != 2 {
		t.Errorf("expected rounding up to 2 tokens, got %d", got)
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
