package oidctoken

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/errors"
)

// flakyProvider serves the key set of a rotating signer, or fails while down,
// counting the requests it receives.
type flakyProvider struct {
	signer *rotatingSigner
	url    string
	down   atomic.Bool
	hits   atomic.Int32
}

func newFlakyProvider(t *testing.T) *flakyProvider {
	t.Helper()
	s, jwksURL := newRotatingSigner(t)
	p := &flakyProvider{signer: s}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		if p.down.Load() {
			http.Error(w, "not found", http.StatusNotFound)
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
	p.url = srv.URL
	return p
}

func (p *flakyProvider) provider() Provider {
	return Provider{ID: "dex", Issuer: testIssuer, JWKSURL: p.url}
}

func bearerRequest(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// A bearer token that is no JWT of the provider, such as a Xolo API key or
// the token of another issuer, never makes the provider be called.
func TestAuthenticateDoesNotCallTheProviderForOtherTokens(t *testing.T) {
	p := newFlakyProvider(t)
	h := NewHandler([]Provider{p.provider()})

	for name, token := range map[string]string{
		"api key":        "xolo-api-key-0123456789",
		"foreign issuer": p.signer.sign(t, "https://other.example.test/"),
	} {
		t.Run(name, func(t *testing.T) {
			user, err := h.Authenticate(httptest.NewRecorder(), bearerRequest(token))
			if user != nil || err != nil {
				t.Fatalf("expected no identity and no error, got user=%v err=%v", user, err)
			}
		})
	}
	if got := p.hits.Load(); got != 0 {
		t.Fatalf("provider called %d times, want 0", got)
	}
}

// While the provider is down and no key set is known, a token of the provider
// is left to the next authenticators instead of failing the request, and the
// provider is called again only once jwksDownloadBackoff has passed.
func TestAuthenticateWhileTheProviderIsDown(t *testing.T) {
	p := newFlakyProvider(t)
	p.down.Store(true)
	h := NewHandler([]Provider{p.provider()})
	token := p.signer.sign(t, testIssuer)

	for range 3 {
		user, err := h.Authenticate(httptest.NewRecorder(), bearerRequest(token))
		if user != nil || err != nil {
			t.Fatalf("expected no identity and no error, got user=%v err=%v", user, err)
		}
	}
	if got := p.hits.Load(); got != 1 {
		t.Fatalf("provider called %d times, want 1 within the backoff", got)
	}

	// Let jwksDownloadBackoff pass, the provider being back.
	cache.mu.Lock()
	cache.failed[p.url] = cache.failed[p.url].Add(-jwksDownloadBackoff)
	cache.mu.Unlock()
	p.down.Store(false)

	user, err := h.Authenticate(httptest.NewRecorder(), bearerRequest(token))
	if err != nil || user == nil {
		t.Fatalf("expected the token accepted once the provider is back, got user=%v err=%v", user, err)
	}
	if got := p.hits.Load(); got != 2 {
		t.Fatalf("provider called %d times, want 2", got)
	}
}

// An expired key set keeps validating tokens while the provider is down, up
// to jwksStaleLimit past its expiry.
func TestValidateTokenUsesTheExpiredKeySetWhileTheProviderIsDown(t *testing.T) {
	p := newFlakyProvider(t)
	h := NewHandler(nil)
	provider := p.provider()

	if _, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), provider); err != nil {
		t.Fatal(err)
	}

	expire := func(by time.Duration) {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		item := cache.items[p.url]
		item.ExpiresAt = time.Now().Add(-by)
		cache.items[p.url] = item
		delete(cache.failed, p.url)
	}

	p.down.Store(true)
	expire(time.Minute)
	if _, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), provider); err != nil {
		t.Fatalf("expected the expired key set to be used, got %v", err)
	}
	if got := p.hits.Load(); got != 2 {
		t.Fatalf("provider called %d times, want 2", got)
	}

	expire(jwksStaleLimit + time.Minute)
	if _, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), provider); !errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("expected ErrKeysUnavailable past the stale limit, got %v", err)
	}
}
