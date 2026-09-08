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
	if trivial["estimated_output_tokens"] != 64.0 {
		t.Errorf("expected the 64-token floor for a trivial prompt, got %v", trivial["estimated_output_tokens"])
	}
	if complex["level"] == "" || complex["word_count"].(float64) <= 0 {
		t.Errorf("expected level and word_count, got %v", complex)
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
		t.Errorf("expected 1000*(0.2+0.65)=850, got %d", got)
	}
}
