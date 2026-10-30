package bridge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
	"github.com/xolo-gateway/xolo/internal/http/middleware/bridge"
)

// lookup runs bridge.Lookup for identity and returns the status and the user
// the terminal handler saw, nil when it was not served.
func lookup(t *testing.T, userStore port.UserStore, identity *authn.User) (int, bool, model.User) {
	t.Helper()
	var (
		served bool
		user   model.User
	)
	handler := bridge.Lookup(userStore)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served, user = true, httpCtx.User(r.Context())
	}))
	req := httptest.NewRequest(http.MethodPost, "/locale", nil)
	ctx := httpCtx.SetBaseURL(req.Context(), "/")
	ctx = httpCtx.SetCurrentURL(ctx, req.URL)
	ctx = httpCtx.SetTenant(ctx, testTenant)
	ctx = authn.SetContextUser(ctx, identity)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req.WithContext(ctx))
	return rec.Code, served, user
}

// failingUserStore answers every lookup with a technical error; any other
// call panics on the nil embedded store.
type failingUserStore struct{ port.UserStore }

func (failingUserStore) GetUserByIdentity(context.Context, model.TenantID, string, string) (model.User, error) {
	return nil, errors.New("database unavailable")
}

func TestLookupResolvesWithoutWriting(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	existing := model.NewUser(testTenantID, "openid-connect", "sub-1", "jane@corp.tld", "Jane", true, model.PlatformRoleUser)
	if err := store.SaveUser(ctx, existing); err != nil {
		t.Fatalf("save user: %v", err)
	}
	foreign := model.NewUser(model.TenantID(model.NewUserID()), "openid-connect", "sub-2", "joe@corp.tld", "Joe", true, model.PlatformRoleUser)
	if err := store.SaveUser(ctx, foreign); err != nil {
		t.Fatalf("save user: %v", err)
	}

	for _, tc := range []struct {
		name     string
		identity *authn.User
		want     model.UserID
	}{
		{"a linked sign-in", newIdentity("sub-1", "changed@corp.tld", "Changed"), existing.ID()},
		{"an API token", &authn.User{AccountID: string(existing.ID())}, existing.ID()},
		{"an unknown sign-in is not created", newIdentity("sub-unknown", "new@corp.tld", "New"), ""},
		{"a token of another tenant", &authn.User{AccountID: string(foreign.ID())}, ""},
		{"an identity without subject", &authn.User{Provider: "openid-connect"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, served, user := lookup(t, store, tc.identity)
			if !served || status != http.StatusOK {
				t.Fatalf("served=%v status=%d", served, status)
			}
			var got model.UserID
			if user != nil {
				got = user.ID()
			}
			if got != tc.want {
				t.Fatalf("user %q, want %q", got, tc.want)
			}
		})
	}

	stored, err := store.GetUserByID(ctx, existing.ID())
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if stored.Email() != "jane@corp.tld" || stored.DisplayName() != "Jane" {
		t.Errorf("the profile was synchronized: %q / %q", stored.Email(), stored.DisplayName())
	}
	if _, err := store.GetUserByIdentity(ctx, testTenantID, "openid-connect", "sub-unknown"); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("an account was created: %v", err)
	}

	t.Run("a technical error is not taken for signed out", func(t *testing.T) {
		status, served, _ := lookup(t, failingUserStore{}, newIdentity("sub-1", "jane@corp.tld", "Jane"))
		if served || status != http.StatusInternalServerError {
			t.Fatalf("served=%v status=%d", served, status)
		}
	})
}
