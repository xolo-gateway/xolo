package gorm_test

import (
	"context"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
)

// newUser builds a user ready to be persisted. NewUser leaves Preferences nil,
// which the GORM mapping dereferences, so every caller has to seed them.
func newUser(subject, email, displayName string, active bool, roles ...string) *model.BaseUser {
	u := model.NewUser(testTenantID, "test", subject, email, displayName, active, roles...)
	u.SetPreferences(model.NewUserPreferences())
	return u
}

func TestUserStore_Lifecycle(t *testing.T) {
	eachBackend(t, scenarioUserStoreLifecycle)
}

func scenarioUserStoreLifecycle(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	// FindOrCreateUser is keyed on (provider, subject) and must not duplicate.
	created, err := store.FindOrCreateUser(ctx, testTenantID, "oidc", "subject-1")
	if err != nil {
		t.Fatalf("FindOrCreateUser: %v", err)
	}
	again, err := store.FindOrCreateUser(ctx, testTenantID, "oidc", "subject-1")
	if err != nil {
		t.Fatalf("FindOrCreateUser (again): %v", err)
	}
	if created.ID() != again.ID() {
		t.Fatalf("expected the same user, got %q then %q", created.ID(), again.ID())
	}

	other, err := store.FindOrCreateUser(ctx, testTenantID, "oidc", "subject-2")
	if err != nil {
		t.Fatalf("FindOrCreateUser (other subject): %v", err)
	}
	if other.ID() == created.ID() {
		t.Fatal("expected a distinct user for a distinct subject")
	}

	// A saved user round-trips with its scalar fields and its roles.
	user := newUser("subject-3", "user@example.com", "Zoé Éloïse", true, "admin", "user")
	if err := store.SaveUser(ctx, user); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}

	loaded, err := store.GetUserByID(ctx, user.ID())
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if loaded.Email() != "user@example.com" {
		t.Errorf("expected email %q, got %q", "user@example.com", loaded.Email())
	}
	if loaded.DisplayName() != "Zoé Éloïse" {
		t.Errorf("expected display name %q, got %q", "Zoé Éloïse", loaded.DisplayName())
	}
	if !loaded.Active() {
		t.Error("expected user to be active")
	}
	if got := len(loaded.Roles()); got != 2 {
		t.Errorf("expected 2 roles, got %d (%v)", got, loaded.Roles())
	}

	// Saving again replaces the role set rather than accumulating it.
	user.SetActive(false)
	if err := store.SaveUser(ctx, newUserWithRoles(user, "user")); err != nil {
		t.Fatalf("SaveUser (update): %v", err)
	}
	loaded, err = store.GetUserByID(ctx, user.ID())
	if err != nil {
		t.Fatalf("GetUserByID (after update): %v", err)
	}
	if loaded.Active() {
		t.Error("expected user to be inactive after update")
	}
	if got := loaded.Roles(); len(got) != 1 || got[0] != "user" {
		t.Errorf("expected roles to be replaced by [user], got %v", got)
	}

	if err := store.DeleteUser(ctx, user.ID()); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := store.GetUserByID(ctx, user.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetUserByID (deleted): expected port.ErrNotFound, got %v", err)
	}
	if err := store.DeleteUser(ctx, user.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("DeleteUser (twice): expected port.ErrNotFound, got %v", err)
	}
}

// newUserWithRoles clones u with a different role set, keeping its identity.
func newUserWithRoles(u *model.BaseUser, roles ...string) *model.BaseUser {
	clone := model.CopyUser(u)
	clone.SetRoles(roles...)
	return clone
}

func TestUserStore_QueryAndCount(t *testing.T) {
	eachBackend(t, scenarioUserStoreQueryAndCount)
}

func scenarioUserStoreQueryAndCount(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	seed := []*model.BaseUser{
		newUser("s-alice", "alice@example.com", "Alice Martin", true, "admin"),
		newUser("s-bob", "bob@example.com", "Bob Durand", true, "user"),
		newUser("s-carol", "carol@example.org", "Carol Bénard", false, "user"),
	}
	for _, u := range seed {
		if err := store.SaveUser(ctx, u); err != nil {
			t.Fatalf("SaveUser(%s): %v", u.Subject(), err)
		}
	}

	tests := []struct {
		name     string
		opts     port.QueryUsersOptions
		expected int64
	}{
		{name: "no filter", opts: port.QueryUsersOptions{}, expected: 3},
		{name: "active only", opts: port.QueryUsersOptions{Active: ptr(true)}, expected: 2},
		{name: "inactive only", opts: port.QueryUsersOptions{Active: ptr(false)}, expected: 1},
		{name: "by role", opts: port.QueryUsersOptions{Roles: []string{"user"}}, expected: 2},
		// LIKE is applied to a lowered column and a lowered term, so the
		// search is case-insensitive on both backends for ASCII. Note that
		// SQLite's LOWER() only folds ASCII, so an accented letter only
		// matches in the case it was stored in — hence "bénard", not "BÉNARD".
		{name: "search on display name", opts: port.QueryUsersOptions{Search: "MARTIN"}, expected: 1},
		{name: "search on accented display name", opts: port.QueryUsersOptions{Search: "bénard"}, expected: 1},
		{name: "search on email domain", opts: port.QueryUsersOptions{Search: "example.org"}, expected: 1},
		{name: "search on subject", opts: port.QueryUsersOptions{Search: "s-bob"}, expected: 1},
		// `_` must be escaped, otherwise it would match any single character.
		{name: "search escapes wildcards", opts: port.QueryUsersOptions{Search: "alice_example"}, expected: 0},
		{name: "search with no match", opts: port.QueryUsersOptions{Search: "nobody"}, expected: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			count, err := store.CountUsers(ctx, test.opts)
			if err != nil {
				t.Fatalf("CountUsers: %v", err)
			}
			if count != test.expected {
				t.Errorf("CountUsers: expected %d, got %d", test.expected, count)
			}

			users, err := store.QueryUsers(ctx, test.opts)
			if err != nil {
				t.Fatalf("QueryUsers: %v", err)
			}
			if int64(len(users)) != test.expected {
				t.Errorf("QueryUsers: expected %d users, got %d", test.expected, len(users))
			}
		})
	}

	// Pagination slices the result set without changing the total count.
	page, err := store.QueryUsers(ctx, port.QueryUsersOptions{Page: ptr(0), Limit: ptr(2)})
	if err != nil {
		t.Fatalf("QueryUsers (page 0): %v", err)
	}
	if len(page) != 2 {
		t.Errorf("expected 2 users on the first page, got %d", len(page))
	}
	total, err := store.CountUsers(ctx, port.QueryUsersOptions{Page: ptr(0), Limit: ptr(2)})
	if err != nil {
		t.Fatalf("CountUsers (paginated): %v", err)
	}
	if total != 3 {
		t.Errorf("expected pagination to be ignored by CountUsers, got %d", total)
	}
}

func TestUserStore_AuthTokens(t *testing.T) {
	eachBackend(t, scenarioUserStoreAuthTokens)
}

func scenarioUserStoreAuthTokens(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	user := newUser("s-token", "token@example.com", "Token Owner", true)
	if err := store.SaveUser(ctx, user); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	valid := model.NewAuthToken(user, org.ID(), "cli", "value-valid", nil)
	if err := store.CreateAuthToken(ctx, valid); err != nil {
		t.Fatalf("CreateAuthToken: %v", err)
	}

	expired := model.NewAuthToken(user, org.ID(), "expired", "value-expired", ptr(time.Now().Add(-time.Hour)))
	if err := store.CreateAuthToken(ctx, expired); err != nil {
		t.Fatalf("CreateAuthToken (expired): %v", err)
	}

	found, err := store.FindAuthToken(ctx, "value-valid")
	if err != nil {
		t.Fatalf("FindAuthToken: %v", err)
	}
	if found.ID() != valid.ID() {
		t.Errorf("expected token %q, got %q", valid.ID(), found.ID())
	}
	if found.Owner() == nil || found.Owner().ID() != user.ID() {
		t.Error("expected the token owner to be preloaded")
	}
	if found.OrgID() != org.ID() {
		t.Errorf("expected org %q, got %q", org.ID(), found.OrgID())
	}

	// An expired token is indistinguishable from a missing one.
	if _, err := store.FindAuthToken(ctx, "value-expired"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("FindAuthToken (expired): expected port.ErrNotFound, got %v", err)
	}
	if _, err := store.FindAuthToken(ctx, "unknown"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("FindAuthToken (unknown): expected port.ErrNotFound, got %v", err)
	}

	tokens, err := store.GetUserAuthTokens(ctx, user.ID())
	if err != nil {
		t.Fatalf("GetUserAuthTokens: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}

	if err := store.DeleteAuthToken(ctx, valid.ID()); err != nil {
		t.Fatalf("DeleteAuthToken: %v", err)
	}
	if err := store.DeleteAuthToken(ctx, valid.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("DeleteAuthToken (twice): expected port.ErrNotFound, got %v", err)
	}

	// Deleting the owner cascades to its remaining tokens.
	if err := store.DeleteUser(ctx, user.ID()); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	tokens, err = store.GetUserAuthTokens(ctx, user.ID())
	if err != nil {
		t.Fatalf("GetUserAuthTokens (after delete): %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("expected the user's tokens to be cascaded away, got %d", len(tokens))
	}
}
