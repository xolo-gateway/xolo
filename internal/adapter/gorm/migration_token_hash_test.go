package gorm

import (
	"testing"

	"github.com/xolo-gateway/xolo/internal/crypto"
)

func TestIsHashedToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  bool
	}{
		{"sha256 digest", crypto.HashToken("some-key"), true},
		{"application key", "xapp_0123456789abcdef0123456789abcdef", false},
		{"base64 user key", "9RvT1n-xQmA0bZkLpWfYdEcHgJiKlMnOpQrStUvWxYz", false},
		{"too short", "abc123", false},
		{"right length, not hex", "z" + crypto.HashToken("x")[1:], false},
		{"uppercase hex", "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHashedToken(tc.value); got != tc.want {
				t.Errorf("isHashedToken(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// The migration must be safe to re-run: hashing an already hashed value would
// lock every caller out.
func TestHashToken_IsIdempotentlyDetectable(t *testing.T) {
	hashed := crypto.HashToken("xapp_secret")

	if !isHashedToken(hashed) {
		t.Fatal("a freshly produced hash must be recognized as hashed")
	}
	if crypto.HashToken(hashed) == hashed {
		t.Fatal("hashing a hash should change it — the guard is what prevents it")
	}
}
