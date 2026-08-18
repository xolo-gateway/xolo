package main

import (
	"testing"

	goanon "github.com/bornholm/go-anon"
	"github.com/bornholm/go-anon/pkg/ner"
)

// detectRegex runs the builtin regex pass alone, without any NER model, which
// is what the recognizer does with the patterns builtinRegexPatterns returns.
func detectRegex(cfg Config, text string) []ner.Entity {
	return ner.RegexEntityFilter(builtinRegexPatterns(cfg))(text, nil)
}

func hasEntity(entities []ner.Entity, typ goanon.EntityType, text string) bool {
	for _, e := range entities {
		if e.Type == typ && e.Text == text {
			return true
		}
	}
	return false
}

func TestBuiltinRegexPatterns_ChecksumRejectsLookalike(t *testing.T) {
	cfg := defaultConfig()

	// 552100554 satisfies the Luhn key, 552100555 does not: only the former is
	// a SIREN, the latter is just a nine-digit reference number.
	entities := detectRegex(cfg, "SIREN 552100554, dossier 552100555")

	if !hasEntity(entities, goanon.TypeSIREN, "552100554") {
		t.Errorf("valid SIREN not detected, got %+v", entities)
	}
	if hasEntity(entities, goanon.TypeSIREN, "552100555") {
		t.Errorf("number with invalid control key detected as SIREN, got %+v", entities)
	}
}

func TestBuiltinRegexPatterns_SirenContextual(t *testing.T) {
	tests := []struct {
		name       string
		contextual bool
		text       string
		want       bool
	}{
		{name: "default keeps bare number", contextual: false, text: "Référence 552100554 du dossier", want: true},
		{name: "contextual drops bare number", contextual: true, text: "Référence 552100554 du dossier", want: false},
		{name: "contextual keeps marked number", contextual: true, text: "SIREN 552100554", want: true},
		{name: "default keeps marked number", contextual: false, text: "SIREN 552100554", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.SirenContextual = test.contextual

			entities := detectRegex(cfg, test.text)

			if got := hasEntity(entities, goanon.TypeSIREN, "552100554"); got != test.want {
				t.Errorf("SIREN detected = %v, want %v (entities: %+v)", got, test.want, entities)
			}
		})
	}
}

func TestBuiltinRegexPatterns_KeepsOtherTypes(t *testing.T) {
	cfg := defaultConfig()
	cfg.SirenContextual = true

	entities := detectRegex(cfg, "Contact : jean@example.com depuis 192.168.1.10")

	if !hasEntity(entities, goanon.TypeEMAIL, "jean@example.com") {
		t.Errorf("email not detected, got %+v", entities)
	}
	if !hasEntity(entities, goanon.TypeIPV4, "192.168.1.10") {
		t.Errorf("IPv4 not detected, got %+v", entities)
	}
	if len(builtinRegexPatterns(cfg)) != len(goanon.BuiltinRegexPatterns) {
		t.Errorf("contextual SIREN should replace the builtin pattern, not add one")
	}
}

func TestPostFilters_MinRunes(t *testing.T) {
	cfg := defaultConfig()
	cfg.MinConfidence = 0
	cfg.MinRunes = 2

	filters := postFilters(cfg)
	if len(filters) != 1 {
		t.Fatalf("len(filters) = %d, want 1", len(filters))
	}

	text := "Éa A"
	got := filters[0](text, []ner.Entity{
		{Type: goanon.TypeLOC, Text: "Éa", Start: 0, End: 3, Confidence: 1},
		{Type: goanon.TypeLOC, Text: "A", Start: 4, End: 5, Confidence: 1},
	})

	// « Éa » counts two runes and survives; the single-character span does not.
	if len(got) != 1 || got[0].Text != "Éa" {
		t.Errorf("filtered entities = %+v, want only Éa", got)
	}
}

func TestPostFilters_Order(t *testing.T) {
	cfg := defaultConfig()
	cfg.MinConfidence = 0.3
	cfg.MinRunes = 2
	cfg.MaxTokens = 4
	cfg.Blocklist = map[string][]string{"PER": {"Monsieur"}}

	if got := len(postFilters(cfg)); got != 4 {
		t.Errorf("len(postFilters) = %d, want 4", got)
	}

	if got := len(postFilters(Config{})); got != 0 {
		t.Errorf("len(postFilters) on empty config = %d, want 0", got)
	}
}
