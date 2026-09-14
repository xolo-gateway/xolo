package main

import (
	"fmt"
	"strings"
	"testing"
)

func fixedTokenFor(tokens map[string]string) func(string) string {
	return func(uuid string) string { return tokens[uuid] }
}

func TestMatcher_CaseInsensitive(t *testing.T) {
	m := buildMatcher([]termEntry{{UUID: "u1", Name: "Jean Dupont"}})
	tokenFor := fixedTokenFor(map[string]string{"u1": "[CLIENT_aaaa]"})

	redacted, used := m.Redact("j'ai vu JEAN DUPONT hier", tokenFor)

	want := "j'ai vu [CLIENT_aaaa] hier"
	if redacted != want {
		t.Errorf("Redact() = %q, want %q", redacted, want)
	}
	if used["[CLIENT_aaaa]"] != "JEAN DUPONT" {
		t.Errorf("used[token] = %q, want the original-case substring %q", used["[CLIENT_aaaa]"], "JEAN DUPONT")
	}
}

func TestMatcher_WordBoundary_NoPartialMatchInsideLongerWord(t *testing.T) {
	m := buildMatcher([]termEntry{{UUID: "u1", Name: "Tilleuls"}})
	tokenFor := fixedTokenFor(map[string]string{"u1": "[PATRIMOINE_bbbb]"})

	text := "Tilleulsville n'est pas concerné"
	redacted, used := m.Redact(text, tokenFor)

	if redacted != text {
		t.Errorf("Redact() = %q, want text left untouched (no partial match inside a longer word)", redacted)
	}
	if len(used) != 0 {
		t.Errorf("used = %v, want empty", used)
	}
}

func TestMatcher_WordBoundary_MatchesAtPunctuation(t *testing.T) {
	m := buildMatcher([]termEntry{{UUID: "u1", Name: "François"}})
	tokenFor := fixedTokenFor(map[string]string{"u1": "[CLIENT_cccc]"})

	redacted, _ := m.Redact("Bonjour François, comment allez-vous ?", tokenFor)

	want := "Bonjour [CLIENT_cccc], comment allez-vous ?"
	if redacted != want {
		t.Errorf("Redact() = %q, want %q", redacted, want)
	}
}

func TestMatcher_LongestMatchWins_NoOverlapDoubleSubstitution(t *testing.T) {
	m := buildMatcher([]termEntry{
		{UUID: "u1", Name: "Les Tilleuls"},
		{UUID: "u2", Name: "Résidence Les Tilleuls"},
	})
	tokenFor := fixedTokenFor(map[string]string{
		"u1": "[PATRIMOINE_1111]",
		"u2": "[PATRIMOINE_2222]",
	})

	redacted, used := m.Redact("Contactez la Résidence Les Tilleuls stp", tokenFor)

	want := "Contactez la [PATRIMOINE_2222] stp"
	if redacted != want {
		t.Errorf("Redact() = %q, want %q (the longer, earlier-starting match should win)", redacted, want)
	}
	if len(used) != 1 {
		t.Fatalf("used = %v, want exactly one entry", used)
	}
	if used["[PATRIMOINE_2222]"] != "Résidence Les Tilleuls" {
		t.Errorf("used[token] = %q, want %q", used["[PATRIMOINE_2222]"], "Résidence Les Tilleuls")
	}
}

func TestMatcher_MultipleDistinctMatches(t *testing.T) {
	m := buildMatcher([]termEntry{
		{UUID: "u1", Name: "Jean Dupont"},
		{UUID: "u2", Name: "Résidence du Parc"},
	})
	tokenFor := fixedTokenFor(map[string]string{
		"u1": "[CLIENT_1111]",
		"u2": "[PATRIMOINE_2222]",
	})

	redacted, used := m.Redact("Jean Dupont habite à la Résidence du Parc.", tokenFor)

	want := "[CLIENT_1111] habite à la [PATRIMOINE_2222]."
	if redacted != want {
		t.Errorf("Redact() = %q, want %q", redacted, want)
	}
	if len(used) != 2 {
		t.Errorf("used = %v, want two entries", used)
	}
}

func TestMatcher_EmptyTermList(t *testing.T) {
	m := buildMatcher(nil)
	text := "rien à masquer ici"
	redacted, used := m.Redact(text, fixedTokenFor(nil))
	if redacted != text {
		t.Errorf("Redact() = %q, want unchanged %q", redacted, text)
	}
	if len(used) != 0 {
		t.Errorf("used = %v, want empty", used)
	}
}

func TestMatcher_SkipsEntriesWithEmptyUUIDOrName(t *testing.T) {
	m := buildMatcher([]termEntry{
		{UUID: "", Name: "Sans UUID"},
		{UUID: "u1", Name: ""},
		{UUID: "u2", Name: "Terme Valide"},
	})
	tokenFor := fixedTokenFor(map[string]string{"u2": "[CLIENT_dddd]"})

	redacted, used := m.Redact("Sans UUID et Terme Valide dans le même texte", tokenFor)

	want := "Sans UUID et [CLIENT_dddd] dans le même texte"
	if redacted != want {
		t.Errorf("Redact() = %q, want %q", redacted, want)
	}
	if len(used) != 1 {
		t.Errorf("used = %v, want exactly one match (the empty-uuid/empty-name entries must be ignored)", used)
	}
}

func TestMatcher_NoMatchLeavesTextUnchanged(t *testing.T) {
	m := buildMatcher([]termEntry{{UUID: "u1", Name: "Client Inconnu"}})
	text := "Ce texte ne contient aucun terme listé."
	redacted, used := m.Redact(text, fixedTokenFor(map[string]string{"u1": "[CLIENT_eeee]"}))
	if redacted != text {
		t.Errorf("Redact() = %q, want unchanged %q", redacted, text)
	}
	if len(used) != 0 {
		t.Errorf("used = %v, want empty", used)
	}
}

func BenchmarkMatcher_Redact(b *testing.B) {
	terms := make([]termEntry, 0, 5000)
	for i := range 5000 {
		terms = append(terms, termEntry{
			UUID: fmt.Sprintf("uuid-%d", i),
			Name: fmt.Sprintf("Résidence Test %d", i),
		})
	}
	m := buildMatcher(terms)
	tokens := make(map[string]string, len(terms))
	for _, t := range terms {
		tokens[t.UUID] = "[X_" + t.UUID + "]"
	}
	tokenFor := fixedTokenFor(tokens)

	text := strings.Repeat("Un texte de conversation banal qui ne contient pas de terme listé. ", 50) +
		"Sauf ici : Résidence Test 4999."

	b.ResetTimer()
	for b.Loop() {
		m.Redact(text, tokenFor)
	}
}
