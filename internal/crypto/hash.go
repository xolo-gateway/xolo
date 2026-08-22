package crypto

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashToken derives the stored form of an API key.
//
// SHA-256 without a salt, deliberately: keys are 256 bits of output from
// crypto/rand, so brute force is out of reach and a per-key salt would only
// prevent the single-query lookup the authentication path relies on. This is
// not a password — a slow KDF would buy nothing here.
func HashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
