package bridge_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/pkg/errors"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
	"github.com/xolo-gateway/xolo/internal/http/middleware/bridge"
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
func call(t *testing.T, store *xologorm.Store, opts bridge.Options, identity *authn.User) callResult {
	t.Helper()
	return callWith(t, store, store, opts, identity)
}

// callWith is call with a distinct invite store, to inject lookup failures.
func callWith(t *testing.T, store *xologorm.Store, inviteStore port.InviteStore, opts bridge.Options, identity *authn.User) callResult {
	t.Helper()

	result := callResult{emitter: &recordingEmitter{}}

	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result.served = true
		result.user = httpCtx.User(r.Context())
	})

	handler := bridge.Middleware(store, inviteStore, result.emitter, opts)(terminal)

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

// failingInviteStore answers every pending-invitation lookup with an error.
// The embedded store stays nil: any other call the bridge makes panics instead
// of silently reaching a database.
type failingInviteStore struct {
	port.InviteStore
}

func (failingInviteStore) ListPendingInvitesForEmail(context.Context, model.TenantID, string) ([]model.InviteToken, error) {
	return nil, errors.New("database unavailable")
}

// stubInviteStore answers every pending-invitation lookup with its invites.
// Like failingInviteStore, any other call panics on the nil embedded store.
type stubInviteStore struct {
	port.InviteStore
	invites []model.InviteToken
}

func (s stubInviteStore) ListPendingInvitesForEmail(context.Context, model.TenantID, string) ([]model.InviteToken, error) {
	return s.invites, nil
}

// exhaustedInvite reports its single use as consumed.
type exhaustedInvite struct{ model.InviteToken }

func (exhaustedInvite) UsesCount() int { return 1 }

// inviteEmail creates an organization in the given tenant and a pending
// invitation targeting email.
func inviteEmail(t *testing.T, store *xologorm.Store, tenantID model.TenantID, orgSlug, email string, expiresAt *time.Time) model.InviteToken {
	t.Helper()

	ctx := context.Background()

	org := model.NewOrganization(tenantID, orgSlug, orgSlug, "")
	if err := store.CreateOrg(ctx, org); err != nil {
		t.Fatalf("create org: %v", err)
	}

	invite := model.NewInviteToken(org.ID(), model.RoleMember, &email, expiresAt, nil, model.NewUserID())
	if err := store.CreateInvite(ctx, invite); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	return invite
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

	// An invitation names its addressee on purpose: refusing it here left the
	// invitee unable to ever reach /join/{token}, which runs behind this
	// middleware.
	t.Run("creates an account for an invited identity", func(t *testing.T) {
		store := newStore(t)

		inviteEmail(t, store, testTenantID, "acme", "jean@corp.tld", nil)

		result := call(t, store, disabled, newIdentity("sub-1", "jean@corp.tld", "Jean"))

		if !result.served {
			t.Fatalf("request should have been served, got status %d", result.status)
		}

		if _, err := store.GetUserByIdentity(ctx, testTenantID, "openid-connect", "sub-1"); err != nil {
			t.Fatalf("get user: %v", err)
		}
	})

	// The invitation grants the account, not its activation. That the inactive
	// invitee can still see, accept and decline the invitation is asserted end
	// to end by TestInvitationHTTPInactiveInvitee (internal/adapter/gorm).
	t.Run("leaves the activation of an invited identity to ActiveByDefault", func(t *testing.T) {
		store := newStore(t)

		opts := disabled
		opts.ActiveByDefault = false

		inviteEmail(t, store, testTenantID, "acme", "jean@corp.tld", nil)

		call(t, store, opts, newIdentity("sub-1", "jean@corp.tld", "Jean"))

		user, err := store.GetUserByIdentity(ctx, testTenantID, "openid-connect", "sub-1")
		if err != nil {
			t.Fatalf("get user: %v", err)
		}
		if user.Active() {
			t.Error("an invitation must not override ActiveByDefault")
		}
	})

	// The administrator's typing and the identity provider's answer rarely
	// agree on case.
	t.Run("matches the invited e-mail case-insensitively", func(t *testing.T) {
		store := newStore(t)

		inviteEmail(t, store, testTenantID, "acme", "Jean.Dupont@corp.tld", nil)

		result := call(t, store, disabled, newIdentity("sub-1", "jean.dupont@corp.tld", "Jean"))

		if !result.served {
			t.Fatalf("request should have been served, got status %d", result.status)
		}
	})

	t.Run("ignores an invitation issued by another tenant", func(t *testing.T) {
		store := newStore(t)

		inviteEmail(t, store, model.TenantID("other-tenant"), "other", "jean@corp.tld", nil)

		result := call(t, store, disabled, newIdentity("sub-1", "jean@corp.tld", "Jean"))

		if result.served {
			t.Error("the request should not have been served")
		}
		if result.status != http.StatusForbidden {
			t.Errorf("status: got %d, want %d", result.status, http.StatusForbidden)
		}
	})

	t.Run("ignores a revoked invitation", func(t *testing.T) {
		store := newStore(t)

		invite := inviteEmail(t, store, testTenantID, "acme", "jean@corp.tld", nil)
		if err := store.RevokeInvite(ctx, invite.ID()); err != nil {
			t.Fatalf("revoke invite: %v", err)
		}

		result := call(t, store, disabled, newIdentity("sub-1", "jean@corp.tld", "Jean"))

		if result.served {
			t.Error("the request should not have been served")
		}
	})

	// An invitation whose uses are exhausted can no longer be accepted, so it
	// provisions nothing. The store already leaves it out of the lookup.
	t.Run("ignores an exhausted invitation", func(t *testing.T) {
		store := newStore(t)

		org := model.NewOrganization(testTenantID, "acme", "Acme", "")
		if err := store.CreateOrg(ctx, org); err != nil {
			t.Fatalf("create org: %v", err)
		}
		email, maxUses := "jean@corp.tld", 1
		invite := model.NewInviteToken(org.ID(), model.RoleMember, &email, nil, &maxUses, model.NewUserID())
		if err := store.CreateInvite(ctx, invite); err != nil {
			t.Fatalf("create invite: %v", err)
		}
		if err := store.IncrementInviteUses(ctx, invite.ID()); err != nil {
			t.Fatalf("increment invite uses: %v", err)
		}

		result := call(t, store, disabled, newIdentity("sub-1", "jean@corp.tld", "Jean"))

		if result.served {
			t.Error("the request should not have been served")
		}
	})

	// The bridge does not rely on the store's filters alone: IsInviteValid
	// rejects an exhausted invitation even if a lookup returns one.
	t.Run("rechecks the validity of what the lookup returns", func(t *testing.T) {
		store := newStore(t)

		email, maxUses := "jean@corp.tld", 1
		exhausted := exhaustedInvite{model.NewInviteToken(model.NewOrgID(), model.RoleMember, &email, nil, &maxUses, model.NewUserID())}
		result := callWith(t, store, stubInviteStore{invites: []model.InviteToken{exhausted}}, disabled, newIdentity("sub-1", "jean@corp.tld", "Jean"))

		if result.served {
			t.Error("the request should not have been served")
		}
		if result.status != http.StatusForbidden {
			t.Errorf("status: got %d, want %d", result.status, http.StatusForbidden)
		}
	})

	// A failed lookup falls back to the configured policy: it must neither
	// grant the account nor turn into a 500.
	t.Run("falls back to the policy when the invite lookup fails", func(t *testing.T) {
		store := newStore(t)

		result := callWith(t, store, failingInviteStore{}, disabled, newIdentity("sub-1", "jean@corp.tld", "Jean"))

		if result.served {
			t.Error("the request should not have been served")
		}
		if result.status != http.StatusForbidden {
			t.Errorf("status: got %d, want %d", result.status, http.StatusForbidden)
		}
	})

	// An open link (no addressee) is not a per-identity grant: it must not act
	// as a back door around AutoCreateUsers.
	t.Run("ignores an untargeted invitation", func(t *testing.T) {
		store := newStore(t)

		org := model.NewOrganization(testTenantID, "acme", "Acme", "")
		if err := store.CreateOrg(ctx, org); err != nil {
			t.Fatalf("create org: %v", err)
		}
		if err := store.CreateInvite(ctx, model.NewInviteToken(org.ID(), model.RoleMember, nil, nil, nil, model.NewUserID())); err != nil {
			t.Fatalf("create invite: %v", err)
		}

		result := call(t, store, disabled, newIdentity("sub-1", "jean@corp.tld", "Jean"))

		if result.served {
			t.Error("the request should not have been served")
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

// The bootstrap path of an empty instance: the configured spelling and the
// provider's rarely agree on case.
func TestDefaultAdminMatchesCaseInsensitively(t *testing.T) {
	store := newStore(t)

	opts := bridge.Options{AutoCreateUsers: false, DefaultAdmins: []string{" Boss@Corp.tld"}}

	result := call(t, store, opts, newIdentity("sub-boss", "boss@corp.tld", "Boss"))

	if !result.served {
		t.Fatalf("request should have been served, got status %d", result.status)
	}
	if !slices.Contains(result.user.Roles(), authz.RoleAdmin) {
		t.Errorf("roles: got %v, want to contain %q", result.user.Roles(), authz.RoleAdmin)
	}
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

// An application's shadow user is a platform artefact whose lifecycle is the
// application's own: the account-provisioning policy must not leave it
// inactive or refuse to create it.
func TestApplicationIdentity(t *testing.T) {
	ctx := context.Background()

	identity := &authn.User{
		Provider:    model.ApplicationProvider,
		Subject:     "app-1",
		DisplayName: "Automata",
	}

	for _, opts := range []bridge.Options{
		{AutoCreateUsers: true, ActiveByDefault: false},
		{AutoCreateUsers: false, ActiveByDefault: false},
	} {
		t.Run(fmt.Sprintf("auto-create=%v active-by-default=%v", opts.AutoCreateUsers, opts.ActiveByDefault), func(t *testing.T) {
			store := newStore(t)

			result := call(t, store, opts, identity)

			if !result.served {
				t.Fatalf("request should have been served, got status %d", result.status)
			}

			user, err := store.GetUserByIdentity(ctx, testTenantID, model.ApplicationProvider, "app-1")
			if err != nil {
				t.Fatalf("get user: %v", err)
			}
			if !user.Active() {
				t.Error("application shadow user should have been created active")
			}
		})
	}
}

// An API token designates its owner: the bridge serves that account as is,
// even when provisioning removed its sign-in link, and never provisions.
func TestAccountIdentity(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	unlinked := model.NewUser(testTenantID, "", "", "owner@corp.tld", "Owner", true, authz.RoleUser)
	if err := store.SaveUser(ctx, unlinked); err != nil {
		t.Fatalf("save user: %v", err)
	}

	result := call(t, store, bridge.Options{}, &authn.User{AccountID: string(unlinked.ID()), Email: "owner@corp.tld"})
	if !result.served || result.user.ID() != unlinked.ID() {
		t.Fatalf("request should have been served as the token owner, got status %d", result.status)
	}

	t.Run("an unknown account is unauthenticated", func(t *testing.T) {
		result := call(t, store, bridge.Options{AutoCreateUsers: true}, &authn.User{AccountID: string(model.NewUserID())})
		if result.served || result.status != http.StatusUnauthorized {
			t.Fatalf("got served=%v status %d, want 401", result.served, result.status)
		}
		if loginFailedReason(result) == "" {
			t.Errorf("a refused token should emit %s", model.EventTypeAuthLoginFailed)
		}
	})
}

// Without provider and subject, an identity would designate every account no
// sign-in is linked to.
func TestIncompleteIdentityIsRefused(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	if err := store.SaveUser(ctx, model.NewUser(testTenantID, "", "", "unlinked@corp.tld", "", true, authz.RoleUser)); err != nil {
		t.Fatalf("save user: %v", err)
	}

	for _, identity := range []*authn.User{
		{Email: "unlinked@corp.tld"},
		{Provider: "openid-connect", Email: "unlinked@corp.tld"},
		{Subject: "sub-1", Email: "unlinked@corp.tld"},
	} {
		result := call(t, store, bridge.Options{AutoCreateUsers: true}, identity)
		if result.served || result.status != http.StatusUnauthorized {
			t.Fatalf("%+v: got served=%v status %d, want 401", identity, result.served, result.status)
		}
		if loginFailedReason(result) == "" {
			t.Errorf("%+v: a refused identity should emit %s", identity, model.EventTypeAuthLoginFailed)
		}
	}
}

// A verified email attaches a sign-in to an account no sign-in is linked to;
// an account already linked elsewhere is never relinked.
func TestVerifiedEmailAttachment(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	provisioned := model.NewUser(testTenantID, "", "", "Jean@corp.tld", "", true, authz.RoleUser)
	if err := store.SaveUser(ctx, provisioned); err != nil {
		t.Fatalf("save user: %v", err)
	}

	verified := newIdentity("sub-1", "jean@corp.tld", "Jean")
	verified.EmailVerified = true
	result := call(t, store, bridge.Options{}, verified)
	if !result.served || result.user.ID() != provisioned.ID() {
		t.Fatalf("a verified email should attach the provisioned account, got status %d", result.status)
	}

	other := newIdentity("sub-2", "jean@corp.tld", "Jean")
	other.EmailVerified = true
	result = call(t, store, bridge.Options{AutoCreateUsers: true}, other)
	if result.served || result.status != http.StatusConflict {
		t.Fatalf("got served=%v status %d, want 409", result.served, result.status)
	}
	if !slices.Contains(result.emitter.types(), model.EventTypeAuthLoginFailed) {
		t.Errorf("a refused sign-in should emit %s, got %v", model.EventTypeAuthLoginFailed, result.emitter.types())
	}
	if reason := loginFailedReason(result); strings.Contains(reason, "email") {
		t.Errorf("an identity conflict must not blame the email, got reason %q", reason)
	}
}

// Only an email held by another account is reported as such; any other
// uniqueness conflict is an identity conflict.
func TestConflictReasons(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	if err := store.SaveUser(ctx, model.NewUser(testTenantID, "openid-connect", "sub-1", "taken@corp.tld", "", true, authz.RoleUser)); err != nil {
		t.Fatalf("save user: %v", err)
	}

	result := call(t, store, bridge.Options{AutoCreateUsers: true}, newIdentity("sub-2", "taken@corp.tld", "Other"))
	if result.served || result.status != http.StatusConflict {
		t.Fatalf("got served=%v status %d, want 409", result.served, result.status)
	}
	if reason := loginFailedReason(result); !strings.Contains(reason, "email") {
		t.Errorf("an email held by another account should be reported, got reason %q", reason)
	}
}

func loginFailedReason(result callResult) string {
	for _, event := range result.emitter.events {
		if event.Type() == model.EventTypeAuthLoginFailed {
			return event.Attributes()["reason"]
		}
	}
	return ""
}
