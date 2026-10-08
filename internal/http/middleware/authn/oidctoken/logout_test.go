package oidctoken

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

const testClientID = "xolo"

func logoutClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":    testIssuer,
		"aud":    testClientID,
		"sub":    "subject",
		"jti":    "jti",
		"iat":    now.Unix(),
		"exp":    now.Add(2 * time.Minute).Unix(),
		"events": map[string]any{backchannelLogoutEvent: map[string]any{}},
	}
}

func TestVerifyLogoutToken(t *testing.T) {
	jwksURL, sign := newSigner(t)
	ctx := context.Background()

	claims, err := VerifyLogoutToken(ctx, sign(logoutClaims()), testIssuer, testClientID, jwksURL)
	require.NoError(t, err)
	require.Equal(t, "subject", claims.Subject)
	require.Equal(t, "jti", claims.ID)

	withSID := logoutClaims()
	withSID["sid"] = "session"
	withSID["azp"] = testClientID
	_, err = VerifyLogoutToken(ctx, sign(withSID), testIssuer, testClientID, jwksURL)
	require.NoError(t, err)

	now := time.Now()
	for name, mutate := range map[string]func(jwt.MapClaims){
		"other issuer":     func(c jwt.MapClaims) { c["iss"] = "https://other.example.test/" },
		"other audience":   func(c jwt.MapClaims) { c["aud"] = "other" },
		"other azp":        func(c jwt.MapClaims) { c["azp"] = "other" },
		"sid only":         func(c jwt.MapClaims) { delete(c, "sub"); c["sid"] = "session" },
		"no jti":           func(c jwt.MapClaims) { delete(c, "jti") },
		"no iat":           func(c jwt.MapClaims) { delete(c, "iat") },
		"no exp":           func(c jwt.MapClaims) { delete(c, "exp") },
		"nonce":            func(c jwt.MapClaims) { c["nonce"] = "n" },
		"no events":        func(c jwt.MapClaims) { delete(c, "events") },
		"other event":      func(c jwt.MapClaims) { c["events"] = map[string]any{"urn:other": map[string]any{}} },
		"extra event":      func(c jwt.MapClaims) { c["events"].(map[string]any)["urn:other"] = map[string]any{} },
		"event not object": func(c jwt.MapClaims) { c["events"] = map[string]any{backchannelLogoutEvent: "yes"} },
		// Still within its lifetime and the clock skew, but past the age
		// that bounds how long a replay key is kept.
		"too old": func(c jwt.MapClaims) {
			c["iat"] = now.Add(-6 * time.Minute).Unix()
			c["exp"] = now.Add(-2 * time.Minute).Unix()
		},
		"too long": func(c jwt.MapClaims) { c["exp"] = now.Add(time.Hour).Unix() },
		"from future": func(c jwt.MapClaims) {
			c["iat"] = now.Add(10 * time.Minute).Unix()
			c["exp"] = now.Add(11 * time.Minute).Unix()
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := logoutClaims()
			mutate(c)
			_, err := VerifyLogoutToken(ctx, sign(c), testIssuer, testClientID, jwksURL)
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrKeysUnavailable)
		})
	}

	t.Run("unsigned", func(t *testing.T) {
		raw, err := jwt.NewWithClaims(jwt.SigningMethodNone, logoutClaims()).SignedString(jwt.UnsafeAllowNoneSignatureType)
		require.NoError(t, err)
		_, err = VerifyLogoutToken(ctx, raw, testIssuer, testClientID, jwksURL)
		require.Error(t, err)
	})
	t.Run("no audience configured", func(t *testing.T) {
		_, err := VerifyLogoutToken(ctx, sign(logoutClaims()), testIssuer, "", jwksURL)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrKeysUnavailable)
	})
	t.Run("keys unavailable", func(t *testing.T) {
		down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unavailable", http.StatusInternalServerError)
		}))
		t.Cleanup(down.Close)
		_, err := VerifyLogoutToken(ctx, sign(logoutClaims()), testIssuer, testClientID, down.URL)
		require.ErrorIs(t, err, ErrKeysUnavailable)
	})
}
