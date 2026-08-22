package gorm_test

import (
	"context"
	"testing"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TestAuthTokenRoundTrip_Hashed asserts a key created through the store still
// authenticates afterwards, and that the clear-text value is not what lands in
// the database.
func TestAuthTokenRoundTrip_Hashed(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()

		user, err := store.FindOrCreateUser(ctx, testTenantID, "oidc", "token-hash-subject")
		if err != nil {
			t.Fatalf("FindOrCreateUser: %v", err)
		}

		const clear = "secret-clear-text-value"
		token := model.NewAuthToken(user, "", "label", clear, nil)
		if err := store.CreateAuthToken(ctx, token); err != nil {
			t.Fatalf("CreateAuthToken: %v", err)
		}

		found, err := store.FindAuthToken(ctx, clear)
		if err != nil {
			t.Fatalf("a key created through the store must still authenticate: %v", err)
		}
		if found.ID() != token.ID() {
			t.Errorf("id: got %v, want %v", found.ID(), token.ID())
		}
		if found.Value() == clear {
			t.Error("the clear-text value was stored as-is")
		}

		// The clear-text value must not be a valid lookup key any more once it
		// is hashed — only the exact key authenticates.
		if _, err := store.FindAuthToken(ctx, clear+"x"); err == nil {
			t.Error("a wrong key authenticated")
		}
	})
}
