package component

import (
	"strings"
	"testing"
)

// TestMaskedToken checks the masked rendering never leaks enough of a key to
// let it be reconstructed, while staying distinguishable between two keys.
func TestMaskedToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{"regular key", "xapp_0123456789abcdef", "xapp_••••••••cdef"},
		{"short body", "xapp_abc", "xapp_••••••••"},
		{"empty", "", "xapp_••••••••"},
		{"no prefix", "0123456789", "xapp_••••••••6789"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := maskedToken(tc.value); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A masked key must never carry the secret part of the value.
func TestMaskedToken_DoesNotLeakBody(t *testing.T) {
	const value = "xapp_deadbeefcafebabe"

	masked := maskedToken(value)
	if masked == value {
		t.Fatal("masked value is the clear-text key")
	}
	if strings.Contains(masked, "deadbeefcafe") {
		t.Errorf("masked value %q still carries the body of the key", masked)
	}
	// Only the last 4 characters are revealed.
	if !strings.HasSuffix(masked, "babe") {
		t.Errorf("masked value %q should end with the last 4 characters", masked)
	}
	if strings.Count(masked, "•") != 8 {
		t.Errorf("masked value %q should hide the body behind 8 bullets", masked)
	}
}
