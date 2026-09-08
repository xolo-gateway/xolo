package main

import (
	"context"
	"encoding/json"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

func score(t *testing.T, messages string) map[string]any {
	t.Helper()
	out, err := (&Plugin{}).PreRequest(context.Background(), &proto.PreRequestInput{MessagesJson: messages})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out.OutputsJson), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPreRequest_ComplexBeatsTrivial(t *testing.T) {
	trivial := score(t, `[{"role":"user","content":"salut"}]`)
	complex := score(t, `[{"role":"user","content":"Rédige une analyse comparative détaillée des architectures hexagonale et en couches, en respectant les contraintes suivantes : 1) citer au moins trois sources, 2) inclure un tableau, 3) conclure par des recommandations chiffrées. Ne dépasse pas 800 mots et utilise un ton formel."}]`)
	if trivial["complexity"].(float64) >= complex["complexity"].(float64) {
		t.Errorf("expected complex > trivial, got %v vs %v", complex["complexity"], trivial["complexity"])
	}
	if trivial["estimated_output_tokens"].(float64) > 128 {
		t.Errorf("expected a short answer for a trivial prompt, got %v", trivial["estimated_output_tokens"])
	}
	if complex["level"] == "" || complex["word_count"].(float64) <= 0 || complex["context_tokens"].(float64) <= 0 {
		t.Errorf("expected level, word_count and context_tokens, got %v", complex)
	}
	if complex["has_code"] != false || complex["constraint_count"].(float64) < 3 {
		t.Errorf("unexpected code/constraint outputs: %v", complex)
	}
}

func TestEstimateOutputTokens(t *testing.T) {
	if got := estimateOutputTokens(0, 0); got != 64 {
		t.Errorf("floor: expected 64, got %d", got)
	}
	if got := estimateOutputTokens(100000, 1); got != 4096 {
		t.Errorf("cap: expected 4096, got %d", got)
	}
	if got := estimateOutputTokens(1000, 0.5); got != 850 {
		t.Errorf("long request: expected 1000*(0.2+0.65)=850, got %d", got)
	}
	if got := estimateOutputTokens(20, 0.8); got != 64+819 {
		t.Errorf("short but demanding request: expected the complexity-driven base, got %d", got)
	}
}

func TestPreRequest_ScoresLastUserTurn(t *testing.T) {
	// A long banal history followed by a demanding question: the score must
	// reflect the question, the context size must reflect the history.
	history := `{"role":"user","content":"salut"},{"role":"assistant","content":"Bonjour ! Comment puis-je vous aider ?"},`
	msgs := "[" + history + history + history + `{"role":"user","content":"Compare les architectures hexagonale et en couches pour un système de paiement soumis à PCI-DSS. Donne les compromis, un tableau, et une recommandation argumentée en 500 mots."}]`
	m := score(t, msgs)
	if m["complexity"].(float64) < 0.7 {
		t.Errorf("expected the last turn to drive complexity, got %v", m["complexity"])
	}
	if m["context_tokens"].(float64) <= m["word_count"].(float64) {
		t.Errorf("context_tokens must cover the whole conversation: %v", m)
	}
}
