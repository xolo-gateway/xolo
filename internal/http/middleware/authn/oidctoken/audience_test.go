package oidctoken

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestValidateTokenAllowedAudiences(t *testing.T) {
	jwksURL, sign := newSigner(t)
	token := func(aud any) string {
		c := jwt.MapClaims{"iss": testIssuer, "sub": "u", "exp": time.Now().Add(time.Hour).Unix()}
		if aud != nil {
			c["aud"] = aud
		}
		return sign(c)
	}

	for _, c := range []struct {
		name    string
		allowed []string
		aud     any
		wantOK  bool
	}{
		{"no allow list accepts any audience", nil, "console-demo", true},
		{"no allow list accepts no audience", nil, nil, true},
		{"allowed audience", []string{"xolo", "chat-demo"}, "chat-demo", true},
		{"one allowed among several", []string{"xolo"}, []string{"other", "xolo"}, true},
		{"foreign audience", []string{"xolo", "chat-demo"}, "console-demo", false},
		{"missing audience", []string{"xolo"}, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := NewHandler(nil)
			user, err := h.validateToken(t.Context(), token(c.aud), Provider{ID: "dex", Issuer: testIssuer, JWKSURL: jwksURL, AllowedAudiences: c.allowed})
			if c.wantOK {
				if err != nil || user == nil {
					t.Fatalf("expected a valid token, got user=%v err=%v", user, err)
				}
				return
			}
			if err != errInvalidToken {
				t.Fatalf("expected errInvalidToken, got user=%v err=%v", user, err)
			}
		})
	}
}

// rotatingSigner serves a JWKS whose single key can be replaced, counting
// the JWKS downloads.
type rotatingSigner struct {
	mu        sync.Mutex
	key       *rsa.PrivateKey
	kid       string
	downloads atomic.Int32
}

func (s *rotatingSigner) rotate(t *testing.T, kid string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.key, s.kid = key, kid
}

func (s *rotatingSigner) sign(t *testing.T, issuer string) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": issuer, "sub": "u", "exp": time.Now().Add(time.Hour).Unix()})
	token.Header["kid"] = s.kid
	signed, err := token.SignedString(s.key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func newRotatingSigner(t *testing.T) (*rotatingSigner, string) {
	t.Helper()
	s := &rotatingSigner{}
	s.rotate(t, "k1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.downloads.Add(1)
		s.mu.Lock()
		defer s.mu.Unlock()
		json.NewEncoder(w).Encode(JWKS{Keys: []JWK{{
			Kty: "RSA", Kid: s.kid, Use: "sig", Alg: "RS256",
			N: base64.RawURLEncoding.EncodeToString(s.key.N.Bytes()),
			E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(s.key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(cache.Clear)
	return s, srv.URL
}

// A key rotated in after the JWKS was cached is picked up by one refetch, and
// further unknown key IDs within the refetch interval do not download again.
func TestValidateTokenRefetchesJWKSOnUnknownKeyID(t *testing.T) {
	s, jwksURL := newRotatingSigner(t)
	h := NewHandler(nil)
	provider := Provider{ID: "dex", Issuer: testIssuer, JWKSURL: jwksURL}

	if _, err := h.validateToken(t.Context(), s.sign(t, testIssuer), provider); err != nil {
		t.Fatal(err)
	}

	s.rotate(t, "k2")
	if _, err := h.validateToken(t.Context(), s.sign(t, testIssuer), provider); err != nil {
		t.Fatalf("rotated key not picked up: %v", err)
	}
	if got := s.downloads.Load(); got != 2 {
		t.Fatalf("downloads = %d, want 2", got)
	}

	s.rotate(t, "k3")
	if _, err := h.validateToken(t.Context(), s.sign(t, testIssuer), provider); err != errInvalidToken {
		t.Fatalf("expected errInvalidToken within the refetch interval, got %v", err)
	}
	if got := s.downloads.Load(); got != 2 {
		t.Fatalf("downloads = %d, want 2 (refetch rate limited)", got)
	}
}

// A token from another issuer never triggers a refetch of this provider's keys.
func TestValidateTokenDoesNotRefetchForForeignIssuer(t *testing.T) {
	s, jwksURL := newRotatingSigner(t)
	h := NewHandler(nil)
	provider := Provider{ID: "dex", Issuer: testIssuer, JWKSURL: jwksURL}

	if _, err := h.validateToken(t.Context(), s.sign(t, testIssuer), provider); err != nil {
		t.Fatal(err)
	}
	s.rotate(t, "k2")
	if _, err := h.validateToken(t.Context(), s.sign(t, "https://other.example.test/"), provider); err != errInvalidToken {
		t.Fatalf("expected errInvalidToken, got %v", err)
	}
	if got := s.downloads.Load(); got != 1 {
		t.Fatalf("downloads = %d, want 1", got)
	}
}

// A failed refetch does not hold the slot for a whole jwksRefetchInterval: a
// rotated key is picked up once jwksRefetchRetryDelay has passed.
func TestValidateTokenRetriesFailedJWKSRefetch(t *testing.T) {
	s, jwksURL := newRotatingSigner(t)
	var failing atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		resp, err := http.Get(jwksURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	h := NewHandler(nil)
	provider := Provider{ID: "dex", Issuer: testIssuer, JWKSURL: srv.URL}

	if _, err := h.validateToken(t.Context(), s.sign(t, testIssuer), provider); err != nil {
		t.Fatal(err)
	}

	s.rotate(t, "k2")
	failing.Store(true)
	if _, err := h.validateToken(t.Context(), s.sign(t, testIssuer), provider); err != errInvalidToken {
		t.Fatalf("expected errInvalidToken while the provider fails, got %v", err)
	}

	// Let jwksRefetchRetryDelay pass.
	cache.mu.Lock()
	cache.refetched[srv.URL] = cache.refetched[srv.URL].Add(-jwksRefetchRetryDelay)
	cache.mu.Unlock()

	failing.Store(false)
	if _, err := h.validateToken(t.Context(), s.sign(t, testIssuer), provider); err != nil {
		t.Fatalf("rotated key not picked up after a failed refetch: %v", err)
	}
}
