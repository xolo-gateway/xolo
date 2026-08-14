package gorm_test

import (
	"context"
	"testing"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
)

// TestUserStore_DuplicateEmailRejected pins the translation of a backend unique
// constraint violation into port.ErrAlreadyExists. The two supported backends
// spell the offending index differently (SQLite reports "users.email",
// PostgreSQL the index name "idx_users_email_nonempty"), so this must hold on
// both: run it with XOLO_TEST_POSTGRES_DSN set to cover the PostgreSQL side.
func TestUserStore_DuplicateEmailRejected(t *testing.T) {
	eachBackend(t, scenarioUserStore_DuplicateEmailRejected)
}

func scenarioUserStore_DuplicateEmailRejected(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	first := model.NewUser(testTenantID, "test", "subject-1", "duplicate@example.com", "First", true)
	first.SetPreferences(model.NewUserPreferences())
	if err := store.SaveUser(ctx, first); err != nil {
		t.Fatalf("SaveUser (first): %v", err)
	}

	second := model.NewUser(testTenantID, "test", "subject-2", "duplicate@example.com", "Second", true)
	second.SetPreferences(model.NewUserPreferences())
	err := store.SaveUser(ctx, second)
	if !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("SaveUser (second): expected port.ErrAlreadyExists, got %v", err)
	}
}

// TestUserStore_EmptyEmailsAllowed guards the partial unique index: users
// authenticated by a provider that exposes no e-mail must still coexist.
func TestUserStore_EmptyEmailsAllowed(t *testing.T) {
	eachBackend(t, scenarioUserStore_EmptyEmailsAllowed)
}

func scenarioUserStore_EmptyEmailsAllowed(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	for _, subject := range []string{"subject-1", "subject-2"} {
		user := model.NewUser(testTenantID, "test", subject, "", "No mail", true)
		user.SetPreferences(model.NewUserPreferences())
		if err := store.SaveUser(ctx, user); err != nil {
			t.Fatalf("SaveUser (%s): %v", subject, err)
		}
	}
}
