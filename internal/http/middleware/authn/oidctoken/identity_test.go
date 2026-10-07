package oidctoken

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testIssuer = "https://id.example.test/"

// newSigner serves a JWKS holding one RSA key and signs tokens with it.
func newSigner(t *testing.T) (string, func(claims jwt.MapClaims) string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(JWKS{Keys: []JWK{{
			Kty: "RSA", Kid: "k1", Use: "sig", Alg: "RS256",
			N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(cache.Clear)
	return srv.URL, func(claims jwt.MapClaims) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "k1"
		signed, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
}

// A validated token carries the email verification, and the issuer when the
// provider proves it.
func TestValidateTokenIdentityProof(t *testing.T) {
	jwksURL, sign := newSigner(t)
	claims := func(verified any) jwt.MapClaims {
		c := jwt.MapClaims{"iss": testIssuer, "sub": "u", "email": "u@example.test", "exp": time.Now().Add(time.Hour).Unix()}
		if verified != nil {
			c["email_verified"] = verified
		}
		return c
	}

	for _, c := range []struct {
		name         string
		proves       bool
		verified     any
		wantIssuer   string
		wantVerified bool
	}{
		{"verified", true, true, testIssuer, true},
		{"verification as a string", true, "true", testIssuer, true},
		{"unverified", true, false, testIssuer, false},
		{"no claim", true, nil, testIssuer, false},
		{"provider proving no issuer", false, true, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := NewHandler(nil)
			user, err := h.validateToken(t.Context(), sign(claims(c.verified)), Provider{ID: "corp", Issuer: testIssuer, JWKSURL: jwksURL, ProvesIssuer: c.proves})
			if err != nil {
				t.Fatal(err)
			}
			if user.Issuer != c.wantIssuer || user.EmailVerified != c.wantVerified || user.Subject != "u" {
				t.Errorf("unexpected user: %+v", user)
			}
		})
	}

	t.Run("another issuer is refused", func(t *testing.T) {
		other := claims(true)
		other["iss"] = "https://other.example.test/"
		if _, err := NewHandler(nil).validateToken(t.Context(), sign(other), Provider{ID: "corp", Issuer: testIssuer, JWKSURL: jwksURL, ProvesIssuer: true}); err == nil {
			t.Fatal("a token of another issuer must be refused")
		}
	})
}
