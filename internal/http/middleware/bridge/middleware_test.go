package bridge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
	"github.com/xolo-gateway/xolo/internal/http/middleware/bridge"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/pkg/errors"
	gormpkg "gorm.io/gorm"
)

// recordingEmitter collects the events emitted during a request.
type recordingEmitter struct {
	events []model.Event
}

func (e *recordingEmitter) Emit(ctx context.Context, event model.Event) {
	e.events = append(e.events, event)
}

func (e *recordingEmitter) types() []string {
	types := make([]string, 0, len(e.events))
	for _, event := range e.events {
		types = append(types, event.Type())
	}
	return types
}

var _ port.EventEmitter = &recordingEmitter{}

func newStore(t *testing.T) *xologorm.Store {
	t.Helper()

	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	return xologorm.NewStore(db)
}

type callResult struct {
	status  int
	served  bool
	user    model.User
	emitter *recordingEmitter
}

// call runs the bridge middleware for the given authenticated identity and
// reports what the terminal handler saw.
func call(t *testing.T, store port.UserStore, opts bridge.Options, identity *authn.User) callResult {
	t.Helper()

	result := callResult{emitter: &recordingEmitter{}}

	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result.served = true
		result.user = httpCtx.User(r.Context())
	})

	handler := bridge.Middleware(store, result.emitter, opts)(terminal)

	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// The error pages are rendered with templ and read the same request-scoped
	// values the HTTP server injects.
	reqCtx := authn.SetContextUser(req.Context(), identity)
	reqCtx = httpCtx.SetBaseURL(reqCtx, "/")
	reqCtx = httpCtx.SetCurrentURL(reqCtx, req.URL)
	// The tenant middleware runs before the bridge in the real chain: without a
	// tenant the bridge has no scope to resolve the identity in.
	reqCtx = httpCtx.SetTenant(reqCtx, testTenant)

	req = req.WithContext(reqCtx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	result.status = rec.Code

	return result
}

func newIdentity(subject, email, displayName string) *authn.User {
	return &authn.User{
		Provider:    "openid-connect",
		Subject:     subject,
		Email:       email,
		DisplayName: displayName,
	}
}

func TestAutoCreateEnabled(t *testing.T) {
	ctx := context.Background()

	t.Run("creates an account for an unknown identity", func(t *testing.T) {
		store := newStore(t)

		result := call(t, store, bridge.Options{AutoCreateUsers: true, ActiveByDefault: true},
			newIdentity("sub-1", "jean@corp.tld", "Jean"))

		if !result.served {
			t.Fatalf("request should have been served, got status %d", result.status)
		}

		user, err := store.GetUserByIdentity(ctx, testTenantID, "openid-connect", "sub-1")
		if err != nil {
			t.Fatalf("get user: %v", err)
		}
		if user.Email() != "jean@corp.tld" || user.DisplayName() != "Jean" {
			t.Errorf("profile: got %q / %q", user.Email(), user.DisplayName())
		}
		if !user.Active() {
			t.Error("user should be active")
		}
		if !slices.Contains(user.Roles(), authz.RoleUser) {
			t.Errorf("roles: got %v, want to contain %q", user.Roles(), authz.RoleUser)
		}
	})

	// ActiveByDefault used to be ignored: the account was always created active.
	t.Run("honours ActiveByDefault", func(t *testing.T) {
		store := newStore(t)

		result := call(t, store, bridge.Options{AutoCreateUsers: true, ActiveByDefault: false},
			newIdentity("sub-1", "jean@corp.tld", "Jean"))

		if !result.served {
			t.Fatalf("request should have been served, got status %d", result.status)
		}

		user, err := store.GetUserByIdentity(ctx, testTenantID, "openid-connect", "sub-1")
		if err != nil {
			t.Fatalf("get user: %v", err)
		}
		if user.Active() {
			t.Error("user should have been created inactive")
		}
	})
}

func TestAutoCreateDisabled(t *testing.T) {
	ctx := context.Background()

	disabled := bridge.Options{AutoCreateUsers: false, ActiveByDefault: true}

	t.Run("rejects an unknown identity", func(t *testing.T) {
		store := newStore(t)

		result := call(t, store, disabled, newIdentity("sub-1", "jean@corp.tld", "Jean"))

		if result.served {
			t.Error("the request should not have been served")
		}
		if result.status != http.StatusForbidden {
			t.Errorf("status: got %d, want %d", result.status, http.StatusForbidden)
		}
		if _, err := store.GetUserByIdentity(ctx, testTenantID, "openid-connect", "sub-1"); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("no user should have been created, got %v", err)
		}
		if types := result.emitter.types(); !slices.Contains(types, model.EventTypeAuthLoginFailed) {
			t.Errorf("emitted events: got %v, want to contain %q", types, model.EventTypeAuthLoginFailed)
		}
	})

	t.Run("accepts a pre-provisioned identity", func(t *testing.T) {
		store := newStore(t)

		provisioned := model.NewUser(testTenantID, "openid-connect", "sub-1", "jean@corp.tld", "Jean", true, model.PlatformRoleUser)
		if err := store.SaveUser(ctx, provisioned); err != nil {
			t.Fatalf("save user: %v", err)
		}

		result := call(t, store, disabled, newIdentity("sub-1", "jean@corp.tld", "Jean"))

		if !result.served {
			t.Fatalf("request should have been served, got status %d", result.status)
		}
		if result.user.ID() != provisioned.ID() {
			t.Errorf("user id: got %q, want %q", result.user.ID(), provisioned.ID())
		}
	})

	// Without this exception, an instance with no user at all could never be
	// bootstrapped.
	t.Run("still creates a default admin", func(t *testing.T) {
		store := newStore(t)

		opts := disabled
		opts.ActiveByDefault = false
		opts.DefaultAdmins = []string{"boss@corp.tld"}

		result := call(t, store, opts, newIdentity("sub-boss", "boss@corp.tld", "Boss"))

		if !result.served {
			t.Fatalf("request should have been served, got status %d", result.status)
		}

		user, err := store.GetUserByIdentity(ctx, testTenantID, "openid-connect", "sub-boss")
		if err != nil {
			t.Fatalf("get user: %v", err)
		}
		if !user.Active() {
			t.Error("a bootstrapped admin must be active, whatever ActiveByDefault says")
		}
		if !slices.Contains(user.Roles(), authz.RoleAdmin) {
			t.Errorf("roles: got %v, want to contain %q", user.Roles(), authz.RoleAdmin)
		}
	})
}

func TestExistingUserSynchronization(t *testing.T) {
	ctx := context.Background()

	t.Run("updates the profile from the identity provider", func(t *testing.T) {
		store := newStore(t)

		existing := model.NewUser(testTenantID, "openid-connect", "sub-1", "old@corp.tld", "Old", true, model.PlatformRoleUser)
		if err := store.SaveUser(ctx, existing); err != nil {
			t.Fatalf("save user: %v", err)
		}

		call(t, store, bridge.Options{AutoCreateUsers: true, ActiveByDefault: true},
			newIdentity("sub-1", "new@corp.tld", "New"))

		user, err := store.GetUserByIdentity(ctx, testTenantID, "openid-connect", "sub-1")
		if err != nil {
			t.Fatalf("get user: %v", err)
		}
		if user.Email() != "new@corp.tld" || user.DisplayName() != "New" {
			t.Errorf("profile: got %q / %q", user.Email(), user.DisplayName())
		}
	})

	// Some authenticators resolve an identity without an e-mail or display
	// name; they must not wipe what is stored.
	t.Run("keeps stored values when the identity carries none", func(t *testing.T) {
		store := newStore(t)

		existing := model.NewUser(testTenantID, "openid-connect", "sub-1", "jean@corp.tld", "Jean", true, model.PlatformRoleUser)
		if err := store.SaveUser(ctx, existing); err != nil {
			t.Fatalf("save user: %v", err)
		}

		call(t, store, bridge.Options{AutoCreateUsers: true, ActiveByDefault: true},
			newIdentity("sub-1", "", ""))

		user, err := store.GetUserByIdentity(ctx, testTenantID, "openid-connect", "sub-1")
		if err != nil {
			t.Fatalf("get user: %v", err)
		}
		if user.Email() != "jean@corp.tld" || user.DisplayName() != "Jean" {
			t.Errorf("profile: got %q / %q", user.Email(), user.DisplayName())
		}
	})
}

// testTenantID is the tenant every fixture of this package belongs to.
// Tenancy is not what these tests exercise: they only need a stable, shared
// owner so the tenant-scoped unique keys behave like the pre-tenant ones.
const testTenantID = model.TenantID("test-tenant")

// testTenant is what the tenant middleware would have injected in the request
// context. Only its identifier matters here: the bridge scopes the identity
// lookup with it and never reads the tenant back from the store.
var testTenant = &stubTenant{id: testTenantID}

type stubTenant struct {
	model.Tenant
	id model.TenantID
}

func (t *stubTenant) ID() model.TenantID { return t.id }
