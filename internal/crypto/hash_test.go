package crypto

import "testing"

func TestHashToken(t *testing.T) {
	const value = "xapp_deadbeef"

	hashed := HashToken(value)

	if hashed == value {
		t.Fatal("hash equals the clear-text value")
	}
	if len(hashed) != 64 {
		t.Errorf("length: got %d, want 64 (hex-encoded SHA-256)", len(hashed))
	}
	if HashToken(value) != hashed {
		t.Error("hashing is not deterministic")
	}
	if HashToken(value+"x") == hashed {
		t.Error("two distinct keys hash to the same value")
	}
}

// A known vector, so an accidental change of algorithm or encoding is caught.
func TestHashToken_KnownVector(t *testing.T) {
	const (
		value = "abc"
		want  = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	)

	if got := HashToken(value); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
