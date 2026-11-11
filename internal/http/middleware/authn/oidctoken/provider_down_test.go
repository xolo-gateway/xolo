package oidctoken

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pkg/errors"
)

// flakyProvider serves the key set of a rotating signer, fails while down,
// and holds its answers while held, counting the requests it receives.
type flakyProvider struct {
	signer *rotatingSigner
	url    string
	down   atomic.Bool
	held   atomic.Pointer[chan struct{}]
	hits   atomic.Int32
}

func newFlakyProvider(t *testing.T) *flakyProvider {
	t.Helper()
	s, jwksURL := newRotatingSigner(t)
	p := &flakyProvider{signer: s}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		if held := p.held.Load(); held != nil {
			<-*held
		}
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
	// Registered after srv.Close, so it runs before it.
	t.Cleanup(p.release)
	p.url = srv.URL
	return p
}

func (p *flakyProvider) provider() Provider {
	return Provider{ID: "dex", Issuer: testIssuer, JWKSURL: p.url}
}

// hold makes the provider answer nothing until release.
func (p *flakyProvider) hold() {
	ch := make(chan struct{})
	p.held.Store(&ch)
}

func (p *flakyProvider) release() {
	if held := p.held.Swap(nil); held != nil {
		close(*held)
	}
}

func (p *flakyProvider) waitForHits(t *testing.T, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for p.hits.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("provider called %d times, want %d", p.hits.Load(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// expire makes the cached key set of the provider expired by that much, and
// forgets the failed downloads.
func (p *flakyProvider) expire(by time.Duration) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	item := cache.items[p.url]
	item.ExpiresAt = time.Now().Add(-by)
	cache.items[p.url] = item
	delete(cache.failed, p.url)
}

// downloadFailed reports whether the last download of the provider's key set
// ended and failed.
func (p *flakyProvider) downloadFailed() bool {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	_, underWay := cache.inflight[p.url]
	_, failed := cache.failed[p.url]
	return failed && !underWay
}

// waitForWaiters waits until want requests wait for the download of the
// provider's key set under way.
func (p *flakyProvider) waitForWaiters(t *testing.T, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cache.mu.RLock()
		d := cache.inflight[p.url]
		cache.mu.RUnlock()
		if d != nil && d.waiters.Load() >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no request waits for the download under way")
		}
		time.Sleep(5 * time.Millisecond)
	}
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

// Without any key set, a request arriving while it is being downloaded waits
// for that download instead of starting its own or failing.
func TestValidateTokenWaitsForTheDownloadUnderWay(t *testing.T) {
	p := newFlakyProvider(t)
	h := NewHandler(nil)
	provider := p.provider()
	p.hold()

	results := make(chan error, 2)
	validate := func() {
		_, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), provider)
		results <- err
	}
	go validate()
	p.waitForHits(t, 1)
	go validate()

	select {
	case err := <-results:
		t.Fatalf("a request did not wait for the download under way: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	p.release()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("expected both requests to get the keys, got %v", err)
		}
	}
	if got := p.hits.Load(); got != 1 {
		t.Fatalf("provider called %d times, want 1", got)
	}
}

// A client giving up while the key set is downloaded for it does not make the
// download fail for the requests waiting for it, nor start the backoff.
func TestValidateTokenDownloadOutlivesTheClientThatStartedIt(t *testing.T) {
	p := newFlakyProvider(t)
	h := NewHandler(nil)
	provider := p.provider()
	p.hold()

	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() {
		_, err := h.validateToken(ctx, p.signer.sign(t, testIssuer), provider)
		first <- err
	}()
	p.waitForHits(t, 1)
	second := make(chan error, 1)
	go func() {
		_, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), provider)
		second <- err
	}()

	cancel()
	if err := <-first; !errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("expected the client that gave up to get ErrKeysUnavailable, got %v", err)
	}
	p.release()
	if err := <-second; err != nil {
		t.Fatalf("expected the waiting request to get the keys, got %v", err)
	}
	if _, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), provider); err != nil {
		t.Fatalf("expected no backoff after the client gave up, got %v", err)
	}
	if got := p.hits.Load(); got != 1 {
		t.Fatalf("provider called %d times, want 1", got)
	}
}

// An expired key set keeps validating tokens at once while a new one is
// downloaded in the background, up to jwksStaleLimit past its expiry.
func TestValidateTokenUsesTheExpiredKeySetWhileTheProviderIsDown(t *testing.T) {
	p := newFlakyProvider(t)
	h := NewHandler(nil)
	provider := p.provider()

	if _, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), provider); err != nil {
		t.Fatal(err)
	}

	// The provider hangs: the request must not wait on it.
	p.down.Store(true)
	p.hold()
	p.expire(time.Minute)
	start := time.Now()
	if _, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), provider); err != nil {
		t.Fatalf("expected the expired key set to be used, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the request waited %s on the provider", elapsed)
	}
	p.waitForHits(t, 2)
	p.release()

	p.expire(jwksStaleLimit + time.Minute)
	if _, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), provider); !errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("expected ErrKeysUnavailable past the stale limit, got %v", err)
	}
}

// A logout token signed by a key the provider rotated in during an outage is
// deferred, not refused: the provider retries it.
func TestVerifyLogoutTokenWithAKeyRotatedDuringAnOutage(t *testing.T) {
	p := newFlakyProvider(t)
	h := NewHandler(nil)
	if _, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), p.provider()); err != nil {
		t.Fatal(err)
	}

	p.signer.rotate(t, "k2")
	p.down.Store(true)
	p.expire(time.Minute)

	raw := signLogout(t, p.signer, logoutClaims())

	for range 2 {
		if _, err := VerifyLogoutToken(t.Context(), raw, testIssuer, testClientID, p.url); !errors.Is(err, ErrKeysUnavailable) {
			t.Fatalf("expected ErrKeysUnavailable, got %v", err)
		}
	}
}

// signLogout signs logout claims with the signer's current key.
func signLogout(t *testing.T, s *rotatingSigner, claims jwt.MapClaims) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = s.kid
	raw, err := token.SignedString(s.key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The logout endpoint is reached without authentication: a token naming an
// unknown key makes the provider be called only when it claims its issuer.
// When no refetch is allowed yet, the logout is deferred whether the cached
// set is fresh or not: it cannot tell the key does not exist.
func TestVerifyLogoutTokenRefetchesOnlyForTheProviderIssuer(t *testing.T) {
	p := newFlakyProvider(t)
	h := NewHandler(nil)
	if _, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), p.provider()); err != nil {
		t.Fatal(err)
	}
	p.signer.rotate(t, "k2")

	foreign := logoutClaims()
	foreign["iss"] = "https://other.example.test/"
	if _, err := VerifyLogoutToken(t.Context(), signLogout(t, p.signer, foreign), testIssuer, testClientID, p.url); err == nil || errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("expected an invalid token, got %v", err)
	}
	if got := p.hits.Load(); got != 1 {
		t.Fatalf("provider called %d times, want 1", got)
	}

	cache.mu.Lock()
	cache.refetched[p.url] = time.Now()
	cache.mu.Unlock()
	if _, err := VerifyLogoutToken(t.Context(), signLogout(t, p.signer, logoutClaims()), testIssuer, testClientID, p.url); !errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("expected ErrKeysUnavailable while no refetch is allowed, got %v", err)
	}
}

// While the provider is down, a logout naming an unknown key does not wait on
// it again within jwksDownloadBackoff: it is deferred at once.
func TestVerifyLogoutTokenDoesNotRefetchWithinTheBackoff(t *testing.T) {
	p := newFlakyProvider(t)
	h := NewHandler(nil)
	if _, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), p.provider()); err != nil {
		t.Fatal(err)
	}
	p.signer.rotate(t, "k2")
	p.down.Store(true)
	p.expire(time.Minute)

	// The background download fails and starts the backoff.
	if _, err := fetchJWKS(t.Context(), p.url); err != nil {
		t.Fatal(err)
	}
	p.waitForHits(t, 2)
	deadline := time.Now().Add(5 * time.Second)
	for !p.downloadFailed() {
		if time.Now().After(deadline) {
			t.Fatal("the background download did not fail")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := VerifyLogoutToken(t.Context(), signLogout(t, p.signer, logoutClaims()), testIssuer, testClientID, p.url); !errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("expected ErrKeysUnavailable, got %v", err)
	}
	if got := p.hits.Load(); got != 2 {
		t.Fatalf("provider called %d times, want 2", got)
	}
}

// A token signed by a key rotated in while the expired key set is being
// refreshed in the background waits for that download instead of being
// refused.
func TestValidateTokenWithAKeyRotatedDuringTheBackgroundRefresh(t *testing.T) {
	p := newFlakyProvider(t)
	h := NewHandler(nil)
	provider := p.provider()
	if _, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), provider); err != nil {
		t.Fatal(err)
	}

	p.signer.rotate(t, "k2")
	p.expire(time.Minute)
	p.hold()
	result := make(chan error, 1)
	go func() {
		_, err := h.validateToken(t.Context(), p.signer.sign(t, testIssuer), provider)
		result <- err
	}()
	p.waitForHits(t, 2)
	p.waitForWaiters(t, 1)
	p.release()

	if err := <-result; err != nil {
		t.Fatalf("expected the token signed by the rotated key accepted, got %v", err)
	}
	if got := p.hits.Load(); got != 2 {
		t.Fatalf("provider called %d times, want 2", got)
	}
}
