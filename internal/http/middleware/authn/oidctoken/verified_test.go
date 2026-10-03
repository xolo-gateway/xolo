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
	"github.com/stretchr/testify/require"
)

func TestLogoutTokenValidation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(JWKS{Keys: []JWK{{Kty: "RSA", Kid: "one", Use: "sig", Alg: "RS256", N: base64.RawURLEncoding.EncodeToString(key.N.Bytes()), E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	defer server.Close()
	valid := func() jwt.MapClaims {
		return jwt.MapClaims{"iss": "https://id.example", "aud": "client", "sub": "person", "jti": "event", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(), "events": map[string]any{"http://schemas.openid.net/event/backchannel-logout": map[string]any{}}}
	}
	for _, tc := range []struct {
		name   string
		change func(jwt.MapClaims)
		valid  bool
	}{
		{"valid", func(jwt.MapClaims) {}, true},
		{"sid with subject", func(c jwt.MapClaims) { c["sid"] = "sid" }, true},
		{"issuer", func(c jwt.MapClaims) { c["iss"] = "https://other" }, false},
		{"audience", func(c jwt.MapClaims) { c["aud"] = "other" }, false},
		{"multiple audiences", func(c jwt.MapClaims) { c["aud"] = []string{"client", "other"} }, false},
		{"authorized party", func(c jwt.MapClaims) { c["azp"] = "other" }, false},
		{"nonce null", func(c jwt.MapClaims) { c["nonce"] = nil }, false},
		{"events null", func(c jwt.MapClaims) { c["events"] = nil }, false},
		{"event null", func(c jwt.MapClaims) {
			c["events"] = map[string]any{"http://schemas.openid.net/event/backchannel-logout": nil}
		}, false},
		{"sid only", func(c jwt.MapClaims) { delete(c, "sub"); c["sid"] = "sid" }, false},
		{"missing replay key", func(c jwt.MapClaims) { delete(c, "jti") }, false},
		{"missing time", func(c jwt.MapClaims) { delete(c, "iat") }, false},
		{"missing expiry", func(c jwt.MapClaims) { delete(c, "exp") }, false},
		{"expired", func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Second).Unix() }, false},
		{"too old", func(c jwt.MapClaims) { c["iat"] = time.Now().Add(-6 * time.Minute).Unix() }, false},
		{"future", func(c jwt.MapClaims) { c["iat"] = time.Now().Add(time.Minute).Unix() }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.change(c)
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
			token.Header["kid"] = "one"
			raw, err := token.SignedString(key)
			require.NoError(t, err)
			_, err = VerifyLogoutToken(t.Context(), raw, "https://id.example", "client", server.URL)
			if tc.valid {
				require.NoError(t, err)
				_, err = VerifyIDToken(t.Context(), raw, "https://id.example", "client", server.URL)
				require.Error(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, valid())
	token.Header["kid"] = "one"
	raw, err := token.SignedString([]byte("bad"))
	require.NoError(t, err)
	_, err = VerifyLogoutToken(t.Context(), raw, "https://id.example", "client", server.URL)
	require.Error(t, err)
}
