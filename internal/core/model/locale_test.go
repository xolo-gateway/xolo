package model

import (
	"strings"
	"testing"
)

func TestIsValidLocale(t *testing.T) {
	for locale, want := range map[string]bool{
		"":                true,
		"fr":              true,
		"fil":             true,
		"es-419":          true,
		"pt-BR":           true,
		"zh-Hant-TW":      true,
		"ast-Latn-419":    true,
		"es-es":           false,
		"ES":              false,
		"es_ES":           false,
		"e":               false,
		"fr-":             false,
		"fr-latn":         false,
		" fr":             false,
		"ast-Latn-4190":   false,
		"toolong-Latn-US": false,
	} {
		if got := IsValidLocale(locale); got != want {
			t.Errorf("IsValidLocale(%q)=%v, want %v", locale, got, want)
		}
	}
}

// TestMaxLocaleLength: the longest tag the pattern accepts, a three-letter
// language with a script and a numeric region, is exactly the cap, so the cap
// never refuses a tag the pattern accepts nor lets a longer one through.
func TestMaxLocaleLength(t *testing.T) {
	longest := "ast-Latn-419"
	if len(longest) != MaxLocaleLength || !IsValidLocale(longest) {
		t.Fatalf("%q (%d bytes) should be valid and %d bytes long", longest, len(longest), MaxLocaleLength)
	}
	if localePattern.MatchString(longest + "0") {
		t.Fatalf("the pattern accepts a tag longer than %d bytes", MaxLocaleLength)
	}
	if IsValidLocale(strings.Repeat("a", MaxLocaleLength+1)) {
		t.Fatalf("a %d byte locale is valid", MaxLocaleLength+1)
	}
}
