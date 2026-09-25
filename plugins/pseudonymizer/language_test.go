package main

import (
	"context"
	"strings"
	"testing"

	goanon "github.com/bornholm/go-anon"
)

func TestDetectionSample_PrefersUserMessages(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "You are a helpful assistant."},
		{"role": "user", "content": "Bonjour, je m'appelle William."},
		{"role": "assistant", "content": "Bonjour William !"},
	}

	got := detectionSample(messages, maxDetectionSample)

	if got != "Bonjour, je m'appelle William." {
		t.Errorf("sample should hold the user message only, got %q", got)
	}
}

func TestDetectionSample_FallsBackOnOtherMessages(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "Tu es un assistant utile."},
		{"role": "user", "content": []any{map[string]any{"type": "image_url"}}},
	}

	if got := detectionSample(messages, maxDetectionSample); got != "Tu es un assistant utile." {
		t.Errorf("sample = %q, want the system message when no user text exists", got)
	}
}

func TestDetectionSample_TextParts(t *testing.T) {
	messages := []map[string]any{
		{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "Analyse ce document."},
				map[string]any{"type": "document", "name": "contrat.pdf"},
			},
		},
	}

	got := detectionSample(messages, maxDetectionSample)

	if got != "Analyse ce document." {
		t.Errorf("sample = %q, want the text part only", got)
	}
}

func TestDetectionSample_RespectsMaxLen(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": strings.Repeat("é", 100)},
		{"role": "user", "content": strings.Repeat("a", 100)},
	}

	got := detectionSample(messages, 32)

	if len(got) > 32 {
		t.Errorf("len(sample) = %d, want <= 32", len(got))
	}
	if !strings.HasPrefix(got, "é") {
		t.Errorf("sample should start with the oldest message, got %q", got)
	}
}

// A new turn must not change the sample once it is full, and must only extend
// it before that: a language that flips between turns changes the instruction
// and the NER model, and breaks the upstream prompt cache (#85).
func TestDetectionSample_StableAcrossTurns(t *testing.T) {
	turns := [][]map[string]any{
		{
			{"role": "user", "content": "Bonjour, peux-tu relire ce module ?"},
		},
		{
			{"role": "assistant", "content": "Here is the review of the module you asked for."},
			{"role": "user", "content": "panic: runtime error: index out of range [3] with length 3"},
		},
		{
			{"role": "assistant", "content": "The slice is shorter than the loop bound."},
			{"role": "user", "content": "Corrige-le."},
		},
	}

	var history []map[string]any
	previous := ""
	for i, turn := range turns {
		history = append(history, turn...)
		sample := detectionSample(history, maxDetectionSample)
		if !strings.HasPrefix(sample, previous) {
			t.Errorf("turn %d: sample does not extend the previous one:\nbefore: %q\nnow:    %q", i+1, previous, sample)
		}
		previous = sample
	}

	full := detectionSample(history[:1], 16)
	if got := detectionSample(history, 16); got != full {
		t.Errorf("a full sample changed with new turns: %q, then %q", full, got)
	}
}

func TestDetectionSample_Empty(t *testing.T) {
	messages := []map[string]any{
		{"role": "user"},
		{"role": "assistant", "content": 42},
	}

	if got := detectionSample(messages, maxDetectionSample); got != "" {
		t.Errorf("sample = %q, want empty", got)
	}
}

func TestDetectLanguage(t *testing.T) {
	candidates := goanon.SupportedLanguages()
	detector := goanon.NewWhatlangDetector(candidates...)

	tests := []struct {
		name         string
		sample       string
		wantLang     string
		wantDetected bool
	}{
		{
			name:         "français",
			sample:       "Bonjour, je m'appelle William Petit et je travaille à Paris depuis dix ans.",
			wantLang:     "fr",
			wantDetected: true,
		},
		{
			name:         "anglais",
			sample:       "Hello, my name is John Smith and I have been working in London for ten years.",
			wantLang:     "en",
			wantDetected: true,
		},
		{
			name:         "espagnol",
			sample:       "Hola, me llamo Juan García y trabajo en Madrid desde hace diez años.",
			wantLang:     "es",
			wantDetected: true,
		},
		{
			name:         "texte vide",
			sample:       "",
			wantLang:     "en",
			wantDetected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Fallback deliberately set to "en" so the fallback path is distinguishable.
			lang, detected := detectLanguage(context.Background(), detector, tt.sample, candidates, "en")
			if lang != tt.wantLang {
				t.Errorf("language = %q, want %q", lang, tt.wantLang)
			}
			if detected != tt.wantDetected {
				t.Errorf("detected = %v, want %v", detected, tt.wantDetected)
			}
		})
	}
}

func TestDetectLanguage_UnsupportedDetectionFallsBack(t *testing.T) {
	// Detector allowed to answer beyond the candidate list handed to detectLanguage.
	detector := goanon.NewWhatlangDetector("fr", "en", "es")

	lang, detected := detectLanguage(context.Background(), detector,
		"Hola, me llamo Juan García y trabajo en Madrid desde hace diez años.",
		[]string{"fr", "en"}, "fr")

	if detected {
		t.Errorf("detected = true, want false for a language outside the candidate list")
	}
	if lang != "fr" {
		t.Errorf("language = %q, want fallback fr", lang)
	}
}

func TestSupportedLanguages_NilStore(t *testing.T) {
	got := supportedLanguages(context.Background(), nil)
	want := goanon.SupportedLanguages()

	if len(got) != len(want) {
		t.Fatalf("languages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("languages = %v, want %v", got, want)
		}
	}
}
