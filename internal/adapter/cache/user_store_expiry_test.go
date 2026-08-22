package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// countingUserStore records how many times the backend was hit, so the tests
// can tell a cache hit from a backend lookup.
type countingUserStore struct {
	port.UserStore
	token model.AuthToken
	calls int
}

func (s *countingUserStore) FindAuthToken(_ context.Context, _ string) (model.AuthToken, error) {
	s.calls++
	if s.token == nil {
		return nil, port.ErrNotFound
	}
	return s.token, nil
}

// TestFindAuthToken_ExpiredCachedTokenIsRejected covers the window where a
// token cached while valid keeps authenticating after its expiry date, for as
// long as the cache TTL — up to an hour with the default configuration.
func TestFindAuthToken_ExpiredCachedTokenIsRejected(t *testing.T) {
	ctx := context.Background()

	expiry := time.Now().Add(50 * time.Millisecond)
	user := model.NewUser("tenant", "provider", "subject", "user@example.com", "User", true)
	token := model.NewAuthToken(user, "org", "label", "secret-value", &expiry)

	backend := &countingUserStore{token: token}
	// A TTL far longer than the token's own expiry is exactly the situation
	// that used to keep an expired token alive.
	store := NewUserStore(backend, 10, time.Hour)

	if _, err := store.FindAuthToken(ctx, "secret-value"); err != nil {
		t.Fatalf("token should be valid before expiry: %v", err)
	}
	if backend.calls != 1 {
		t.Fatalf("backend calls: got %d, want 1", backend.calls)
	}

	time.Sleep(80 * time.Millisecond)

	// The backend would now report the token as absent; make it report the
	// token as still present to prove the cache path does its own check.
	_, err := store.FindAuthToken(ctx, "secret-value")
	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("expired token served from cache: got %v, want port.ErrNotFound", err)
	}
}

func TestFindAuthToken_TokenWithoutExpiryStaysValid(t *testing.T) {
	ctx := context.Background()

	user := model.NewUser("tenant", "provider", "subject", "user@example.com", "User", true)
	token := model.NewAuthToken(user, "org", "label", "secret-value", nil)

	backend := &countingUserStore{token: token}
	store := NewUserStore(backend, 10, time.Hour)

	for i := range 2 {
		if _, err := store.FindAuthToken(ctx, "secret-value"); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
	}

	if backend.calls != 1 {
		t.Errorf("backend calls: got %d, want 1 (second call should hit the cache)", backend.calls)
	}
}
