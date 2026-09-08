package main

import (
	"context"
	"encoding/json"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

func classifyText(t *testing.T, p *Plugin, config, text string) map[string]any {
	t.Helper()
	msgs, _ := json.Marshal([]map[string]string{{"role": "user", "content": text}})
	out, err := p.PreRequest(context.Background(), &proto.PreRequestInput{
		Ctx:          &proto.RequestContext{ConfigJson: config},
		MessagesJson: string(msgs),
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

func TestRules_DecideExplicitRequests(t *testing.T) {
	p := &Plugin{}
	cases := map[string]string{
		"Traduis en anglais : bonjour, comment allez-vous ?":                              "translation",
		"Résume cet article en trois phrases.":                                            "summarization",
		"```go\nfunc main() {}\n```\nPourquoi ça ne compile pas ?":                        "code",
		"Reformule ce paragraphe dans un style plus formel.":                              "rewriting",
		"Résous l'équation 3x² − 5x + 2 = 0.":                                             "math",
		"Écris un poème de quatre vers sur la pluie.":                                     "creative",
		"Comment configurer une adresse IP fixe sur Ubuntu ?":                             "instruction",
		"Compare les architectures hexagonale et en couches pour un système de paiement.": "analysis",
	}
	for text, want := range cases {
		m := classifyText(t, p, "", text)
		if m["category"] != want {
			t.Errorf("%q: expected %s, got %v (source %v)", text, want, m["category"], m["source"])
		}
	}
}

func TestShortChat_IsConversation(t *testing.T) {
	m := classifyText(t, &Plugin{}, "", "Salut, ça va ?")
	if m["category"] != "conversation" {
		t.Errorf("expected conversation, got %v", m)
	}
}

func TestModel_FallsBackWhenRulesDisabled(t *testing.T) {
	p := &Plugin{}
	m := classifyText(t, p, `{"use_rules":false}`, "Translate into French: the meeting has been postponed until next week.")
	if m["source"] != "model" {
		t.Errorf("expected the model to decide, got %v", m)
	}
	if m["category"] != "translation" {
		t.Errorf("expected translation, got %v", m)
	}
	if c := m["confidence"].(float64); c <= 0 || c > 1 {
		t.Errorf("confidence must be in (0,1], got %v", c)
	}
	if p.model == nil || p.loadErr != nil {
		t.Fatalf("model should be loaded once without error: %v", p.loadErr)
	}
}

func TestMinConfidence_YieldsUnknown(t *testing.T) {
	m := classifyText(t, &Plugin{}, `{"use_rules":false,"min_confidence":1}`, "Quelle est la capitale de l'Australie ?")
	if m["category"] != "unknown" {
		t.Errorf("a margin below the threshold must yield unknown, got %v", m)
	}
}

func TestEmptyPrompt_IsUnknown(t *testing.T) {
	out, err := (&Plugin{}).PreRequest(context.Background(), &proto.PreRequestInput{MessagesJson: `[]`})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(out.OutputsJson), &m)
	if m["category"] != "unknown" || m["confidence"] != 0.0 {
		t.Errorf("expected unknown/0, got %v", m)
	}
}

func TestParseConfig(t *testing.T) {
	if c := parseConfig(""); !c.UseRules || c.MinConfidence != 0.05 {
		t.Errorf("defaults: %+v", c)
	}
	if c := parseConfig(`{"min_confidence":0.3}`); !c.UseRules || c.MinConfidence != 0.3 {
		t.Errorf("use_rules must stay true when absent: %+v", c)
	}
	if c := parseConfig(`{"min_confidence":7}`); c.MinConfidence != 0.05 {
		t.Errorf("out-of-range values fall back: %+v", c)
	}
}
