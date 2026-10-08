package gorm_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/adapter/cache"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	gormpkg "gorm.io/gorm"
)

const testIssuer = "https://id.example.test/"

// Two local providers prove the same issuer; GitHub OAuth proves none.
var testIssuers = model.IdentityIssuers{"oidc": testIssuer, "oidc-alias": testIssuer}

func newIdentityStore(t *testing.T, db *gormpkg.DB) *xologorm.Store {
	t.Helper()
	store := xologorm.NewStore(db, xologorm.WithIdentityIssuers(func() model.IdentityIssuers { return testIssuers }))
	require.NoError(t, store.Migrate(context.Background()))
	tenant := model.NewTenant("test", "Test", "")
	tenant.SetID(testTenantID)
	require.NoError(t, store.CreateTenant(context.Background(), tenant))
	return store
}

func newIdentityService(store *xologorm.Store, opts ...service.ProvisioningServiceOptionFunc) *service.ProvisioningService {
	opts = append([]service.ProvisioningServiceOptionFunc{service.WithProvisioningTransaction(store), service.WithProvisioningReader(store), service.WithMultiTenant(true)}, opts...)
	return service.NewProvisioningService(store, store, store, store, opts...)
}

func declare(subject string) service.IdentityChange {
	return service.IdentityChange{Kind: service.IdentityDeclare, Identity: model.Identity{Issuer: testIssuer, Subject: subject}}
}

func putMember(svc *service.ProvisioningService, tenantID model.TenantID, id model.UserID, email string, identity service.IdentityChange) error {
	_, err := svc.PutTenantMember(context.Background(), tenantID, id, service.CommonMember{
		Email: email, DisplayName: "Member", TenantRole: model.TenantRoleMember, Status: model.StatusActive, Identity: identity,
	})
	return err
}

func signIn(subject, email string, verified bool) service.AuthenticatedIdentity {
	return service.AuthenticatedIdentity{Provider: "oidc", Issuer: testIssuer, Subject: subject, Email: email, EmailVerified: verified}
}

var (
	openPolicy   = service.LoginPolicy{AutoCreate: true, ActiveByDefault: true}
	closedPolicy = service.LoginPolicy{}
)

func memberProjection(t *testing.T, store *xologorm.Store, tenantID model.TenantID, id model.UserID) model.CommonItem {
	t.Helper()
	item, err := store.ReadProjection(context.Background(), model.CommonScope{Family: model.FamilyMember, TenantID: string(tenantID)}, string(id))
	require.NoError(t, err)
	return item
}

// signInActors returns the users named as actor by the audits of an account:
// provisioning writes name no user, sign-ins name one.
func signInActors(t *testing.T, db *gormpkg.DB, id model.UserID) []model.UserID {
	t.Helper()
	var audits []xologorm.MutationAudit
	require.NoError(t, db.Where("resource = ? AND resource_id = ?", "user", string(id)).Find(&audits).Error)
	var actors []model.UserID
	for _, audit := range audits {
		var actor model.Actor
		require.NoError(t, json.Unmarshal([]byte(audit.Actor), &actor))
		if actor.UserID != "" {
			actors = append(actors, actor.UserID)
		}
	}
	return actors
}

func auditCount(t *testing.T, db *gormpkg.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&xologorm.MutationAudit{}).Count(&n).Error)
	return n
}

// TestDeclaredIdentity follows a declared identity from its PUT to the
// sign-ins it attaches, then through its removal.
func TestDeclaredIdentity(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newIdentityStore(t, db)
		svc := newIdentityService(store)
		resolver := service.NewIdentityResolver(store, store, store)
		alice := model.NewUserID()

		require.NoError(t, putMember(svc, testTenantID, alice, "alice@example.test", declare("Alice ")))
		declared := memberProjection(t, store, testTenantID, alice)
		var rep map[string]any
		require.NoError(t, json.Unmarshal(declared.Representation, &rep))
		require.Equal(t, map[string]any{"issuer": testIssuer, "subject": "Alice "}, rep["identity"])

		found, err := store.GetUserByDeclaredIdentity(ctx, testTenantID, model.Identity{Issuer: testIssuer, Subject: "Alice "})
		require.NoError(t, err)
		require.Equal(t, alice, found.ID())
		_, err = store.GetUserByDeclaredIdentity(ctx, testTenantID, model.Identity{Issuer: testIssuer, Subject: "alice "})
		require.ErrorIs(t, err, port.ErrNotFound, "the subject is exact")

		require.NoError(t, putMember(svc, testTenantID, alice, "alice@example.test", declare("Alice ")))
		require.Equal(t, declared.ETag, memberProjection(t, store, testTenantID, alice).ETag, "a repeated PUT is a no-op")

		require.ErrorIs(t, putMember(svc, testTenantID, model.NewUserID(), "other@example.test", declare("Alice ")), port.ErrAlreadyExists)

		// The first sign-in links the declared account, without changing its
		// projection: the link is not part of the member.
		audits := auditCount(t, db)
		user, err := resolver.Resolve(ctx, testTenantID, signIn("Alice ", "", false), closedPolicy)
		require.NoError(t, err)
		require.Equal(t, alice, user.ID())
		stored, err := store.GetUserByID(ctx, alice)
		require.NoError(t, err)
		require.Equal(t, "oidc", stored.Provider())
		require.Equal(t, "Alice ", stored.Subject())
		require.Equal(t, declared.ETag, memberProjection(t, store, testTenantID, alice).ETag)
		require.Equal(t, audits+1, auditCount(t, db))
		require.Equal(t, []model.UserID{alice}, signInActors(t, db, alice), "the linked account is the actor")

		user, err = resolver.Resolve(ctx, testTenantID, signIn("Alice ", "", false), closedPolicy)
		require.NoError(t, err)
		require.Equal(t, alice, user.ID())
		require.Equal(t, audits+1, auditCount(t, db), "a sign-in that changes nothing writes nothing")

		// A different identity is never assigned over the link.
		require.ErrorIs(t, putMember(svc, testTenantID, alice, "alice@example.test", declare("Mallory")), port.ErrAlreadyExists)
		require.Equal(t, declared.ETag, memberProjection(t, store, testTenantID, alice).ETag)

		// Keeping the identity while changing the email preserves the link.
		require.NoError(t, putMember(svc, testTenantID, alice, "alice@new.example.test", service.IdentityChange{}))
		stored, err = store.GetUserByID(ctx, alice)
		require.NoError(t, err)
		require.Equal(t, "Alice ", stored.Subject())
		require.NotNil(t, stored.DeclaredIdentity())

		// null removes the declaration and detaches the link.
		require.NoError(t, putMember(svc, testTenantID, alice, "alice@new.example.test", service.IdentityChange{Kind: service.IdentityUnlink}))
		stored, err = store.GetUserByID(ctx, alice)
		require.NoError(t, err)
		require.Empty(t, stored.Provider())
		require.Empty(t, stored.Subject())
		require.Nil(t, stored.DeclaredIdentity())
		require.NotContains(t, string(memberProjection(t, store, testTenantID, alice).Representation), "identity")

		_, err = resolver.Resolve(ctx, testTenantID, signIn("Alice ", "alice@new.example.test", false), closedPolicy)
		require.ErrorIs(t, err, service.ErrAccountCreationDisabled, "an unverified email attaches nothing")
		user, err = resolver.Resolve(ctx, testTenantID, signIn("Alice ", "Alice@New.Example.test", true), closedPolicy)
		require.NoError(t, err)
		require.Equal(t, alice, user.ID(), "a verified email reattaches the account")

		var payloads []string
		require.NoError(t, db.Model(&xologorm.ProvisioningEvent{}).Pluck("payload", &payloads).Error)
		for _, payload := range payloads {
			require.NotContains(t, payload, "id.example.test", "events carry keys and ETags only")
		}
	})
}

// TestDeclaredIdentityPerTenant lets the same identity own a distinct member
// in every tenant.
func TestDeclaredIdentityPerTenant(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newIdentityStore(t, db)
		svc := newIdentityService(store)
		other := model.NewTenant("other", "Other", "")
		require.NoError(t, store.CreateTenant(ctx, other))

		first, second := model.NewUserID(), model.NewUserID()
		require.NoError(t, putMember(svc, testTenantID, first, "same@example.test", declare("same")))
		require.NoError(t, putMember(svc, other.ID(), second, "same@example.test", declare("same")))

		resolver := service.NewIdentityResolver(store, store, store)
		a, err := resolver.Resolve(ctx, testTenantID, signIn("same", "", false), closedPolicy)
		require.NoError(t, err)
		b, err := resolver.Resolve(ctx, other.ID(), signIn("same", "", false), closedPolicy)
		require.NoError(t, err)
		require.Equal(t, first, a.ID())
		require.Equal(t, second, b.ID())
	})
}

// TestIdentityOwnedOnce keeps one identity designating one account of a
// tenant, whether declared or linked, through every write path.
func TestIdentityOwnedOnce(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newIdentityStore(t, db)
		svc := newIdentityService(store)

		// A link through another provider proving the same issuer is the
		// same identity.
		linked := model.NewUser(testTenantID, "oidc-alias", "bob", "bob@example.test", "", true, model.PlatformRoleUser)
		require.NoError(t, store.SaveUser(ctx, linked))
		require.ErrorIs(t, putMember(svc, testTenantID, model.NewUserID(), "bob2@example.test", declare("bob")), port.ErrAlreadyExists)

		// A declaration must match the link of its own account, and GitHub
		// proves no issuer.
		github := model.NewUser(testTenantID, "github", "carol", "carol@example.test", "", true, model.PlatformRoleUser)
		require.NoError(t, store.SaveUser(ctx, github))
		require.ErrorIs(t, putMember(svc, testTenantID, github.ID(), "carol@example.test", declare("carol")), port.ErrAlreadyExists)
		require.NoError(t, putMember(svc, testTenantID, linked.ID(), "bob@example.test", declare("bob")), "its own matching link is kept")

		require.NoError(t, putMember(svc, testTenantID, model.NewUserID(), "dave@example.test", declare("dave")))
		_, err := store.FindOrCreateUser(ctx, testTenantID, "oidc-alias", "dave")
		require.ErrorIs(t, err, port.ErrAlreadyExists)
		_, _, err = svc.ProvisionUser(ctx, testTenantID, service.UserIdentityParams{Provider: "oidc", Subject: "dave"})
		require.ErrorIs(t, err, port.ErrAlreadyExists)

		// An account no sign-in is linked to never answers for an identity.
		_, err = store.GetUserByIdentity(ctx, testTenantID, "", "")
		require.ErrorIs(t, err, port.ErrNotFound)

		// A rolled back transaction leaves the declaration and the link.
		boom := errors.New("boom")
		err = store.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
			user, err := tx.GetUserByID(ctx, linked.ID())
			if err != nil {
				return err
			}
			unlinked := model.CopyUser(user)
			unlinked.SetDeclaredIdentity(nil)
			unlinked.SetAuthenticationLink("", "")
			if err := tx.SaveUser(ctx, unlinked); err != nil {
				return err
			}
			return boom
		})
		require.ErrorIs(t, err, boom)
		stored, err := store.GetUserByID(ctx, linked.ID())
		require.NoError(t, err)
		require.Equal(t, "bob", stored.Subject())
		require.NotNil(t, stored.DeclaredIdentity())
	})
}

// TestFindUsersByEmail matches normalized emails without rewriting them.
func TestFindUsersByEmail(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newIdentityStore(t, db)
		for i, email := range []string{"Jean@Example.test", "jean@example.test", "Élodie@example.test", " kim@example.test", "other@example.test"} {
			user := model.NewUser(testTenantID, "oidc", "email-"+string(rune('a'+i)), email, "", true, model.PlatformRoleUser)
			require.NoError(t, store.SaveUser(ctx, user))
		}

		emails := func(email string, limit int) []string {
			users, err := store.FindUsersByEmail(ctx, testTenantID, email, limit)
			require.NoError(t, err)
			var found []string
			for _, u := range users {
				found = append(found, u.Email())
			}
			return found
		}
		require.ElementsMatch(t, []string{"Jean@Example.test", "jean@example.test"}, emails("JEAN@example.TEST", 5), "historical case variants are both kept")
		require.Len(t, emails("jean@example.test", 1), 1)
		require.Equal(t, []string{"Élodie@example.test"}, emails("élodie@EXAMPLE.test", 5))
		require.Equal(t, []string{" kim@example.test"}, emails("Kim@example.test ", 5))
		require.Empty(t, emails("nobody@example.test", 5))
		require.Empty(t, emails("   ", 5))
		loaded, err := store.FindUsersByEmail(ctx, testTenantID, "élodie@example.test", 5)
		require.NoError(t, err)
		require.Len(t, loaded, 1)
		require.Equal(t, []string{model.PlatformRoleUser}, loaded[0].Roles(), "the matching account is loaded whole")

		// Provisioning never creates a new ambiguity.
		svc := newIdentityService(store)
		require.ErrorIs(t, putMember(svc, testTenantID, model.NewUserID(), "OTHER@example.test", service.IdentityChange{}), port.ErrAlreadyExists)
	})
}

// TestIdentityResolution covers the refusals of the sign-in resolution.
func TestIdentityResolution(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newIdentityStore(t, db)
		svc := newIdentityService(store)
		resolver := service.NewIdentityResolver(store, store, store)

		t.Run("an ambiguous verified email is refused without merging", func(t *testing.T) {
			for _, email := range []string{"Twin@example.test", "twin@example.test"} {
				require.NoError(t, store.SaveUser(ctx, model.NewUser(testTenantID, "", "", email, "", true, model.PlatformRoleUser)))
			}
			_, err := resolver.Resolve(ctx, testTenantID, signIn("twin", "TWIN@example.test", true), openPolicy)
			require.ErrorIs(t, err, service.ErrIdentityConflict)
			_, err = store.GetUserByIdentity(ctx, testTenantID, "oidc", "twin")
			require.ErrorIs(t, err, port.ErrNotFound)
		})

		t.Run("a platform administrator is never attached", func(t *testing.T) {
			admin := model.NewUser(testTenantID, "", "", "root@example.test", "", true, model.PlatformRoleUser, model.PlatformRoleAdmin)
			admin.SetDeclaredIdentity(&model.Identity{Issuer: testIssuer, Subject: "root"})
			require.NoError(t, store.SaveUser(ctx, admin))
			_, err := resolver.Resolve(ctx, testTenantID, signIn("root", "", false), openPolicy)
			require.ErrorIs(t, err, service.ErrIdentityConflict)

			unlinked := model.NewUser(testTenantID, "", "", "root2@example.test", "", true, model.PlatformRoleUser, model.PlatformRoleAdmin)
			require.NoError(t, store.SaveUser(ctx, unlinked))
			_, err = resolver.Resolve(ctx, testTenantID, signIn("root2", "root2@example.test", true), openPolicy)
			require.ErrorIs(t, err, service.ErrIdentityConflict)
		})

		t.Run("a declared account waits for its own identity", func(t *testing.T) {
			require.NoError(t, putMember(svc, testTenantID, model.NewUserID(), "erin@example.test", declare("erin")))
			_, err := resolver.Resolve(ctx, testTenantID, signIn("not-erin", "erin@example.test", true), openPolicy)
			require.ErrorIs(t, err, service.ErrIdentityConflict)
		})

		t.Run("a linked account is never relinked by email", func(t *testing.T) {
			require.NoError(t, store.SaveUser(ctx, model.NewUser(testTenantID, "oidc", "frank", "frank@example.test", "", true, model.PlatformRoleUser)))
			_, err := resolver.Resolve(ctx, testTenantID, signIn("frank-2", "frank@example.test", true), openPolicy)
			require.ErrorIs(t, err, service.ErrIdentityConflict)
		})

		t.Run("an unverified email never attaches", func(t *testing.T) {
			require.NoError(t, store.SaveUser(ctx, model.NewUser(testTenantID, "", "", "gina@example.test", "", true, model.PlatformRoleUser)))
			_, err := resolver.Resolve(ctx, testTenantID, signIn("gina", "gina@example.test", false), openPolicy)
			require.ErrorIs(t, err, port.ErrAlreadyExists, "a new account can not take the email either")
		})

		t.Run("an identity without a proven issuer ignores declarations", func(t *testing.T) {
			require.NoError(t, putMember(svc, testTenantID, model.NewUserID(), "hank@example.test", declare("hank")))
			_, err := resolver.Resolve(ctx, testTenantID, service.AuthenticatedIdentity{Provider: "github", Subject: "hank"}, closedPolicy)
			require.ErrorIs(t, err, service.ErrAccountCreationDisabled)
		})

		t.Run("concurrent first sign-ins create one account", func(t *testing.T) {
			var wg sync.WaitGroup
			ids := make(chan model.UserID, 8)
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					user, err := resolver.Resolve(context.Background(), testTenantID, signIn("zed", "zed@example.test", true), openPolicy)
					if err != nil {
						t.Errorf("resolve: %v", err)
						return
					}
					ids <- user.ID()
				}()
			}
			wg.Wait()
			close(ids)
			first := <-ids
			for id := range ids {
				require.Equal(t, first, id)
			}
			var n int64
			require.NoError(t, db.Model(&xologorm.User{}).Where("subject = ?", "zed").Count(&n).Error)
			require.EqualValues(t, 1, n)
		})
	})
}

// TestIdentityResolutionAgainstProvisioning races a declaration with the
// sign-in of the same identity: whatever the order, one account owns it.
func TestIdentityResolutionAgainstProvisioning(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newIdentityStore(t, db)
		svc := newIdentityService(store)
		resolver := service.NewIdentityResolver(store, store, store)
		for i := range 5 {
			subject := "race-" + uuid.NewString()
			member := model.NewUserID()
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				err := putMember(svc, testTenantID, member, "race"+string(rune('a'+i))+"@example.test", declare(subject))
				if err != nil && !errors.Is(err, port.ErrAlreadyExists) {
					t.Errorf("put: %v", err)
				}
			}()
			go func() {
				defer wg.Done()
				if _, err := resolver.Resolve(context.Background(), testTenantID, signIn(subject, "", false), openPolicy); err != nil {
					t.Errorf("resolve: %v", err)
				}
			}()
			wg.Wait()
			var n int64
			require.NoError(t, db.Model(&xologorm.User{}).
				Where("(identity_issuer = ? AND identity_subject = ?) OR (provider = ? AND subject = ?)", testIssuer, subject, "oidc", subject).
				Count(&n).Error)
			require.EqualValues(t, 1, n, "one account owns the identity")
		}
	})
}

// TestIdentityResolutionSkipsFeedLock: requests of a linked account, and the
// first sign-in of a declared one, never wait for a publisher.
func TestIdentityResolutionSkipsFeedLock(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		if db.Dialector.Name() != "postgres" {
			t.Skip("SQLite serializes every writer")
		}
		store := newIdentityStore(t, db)
		svc := newIdentityService(store)
		resolver := service.NewIdentityResolver(store, store, store)
		org := model.NewOrganization(testTenantID, "hot", "Hot", "")
		require.NoError(t, store.CreateOrg(t.Context(), org))
		require.NoError(t, store.SaveUser(t.Context(), model.NewUser(testTenantID, "oidc", "linked", "linked@example.test", "Linked", true, model.PlatformRoleUser)))
		require.NoError(t, putMember(svc, testTenantID, model.NewUserID(), "declared@example.test", declare("declared")))
		before := eventCount(t, db)

		holdFeedLock(t, db, org.ID())
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		for range 3 {
			_, err := resolver.Resolve(ctx, testTenantID, service.AuthenticatedIdentity{Provider: "oidc", Issuer: testIssuer, Subject: "linked", Email: "linked@example.test", DisplayName: "Linked"}, closedPolicy)
			require.NoError(t, err)
		}
		_, err := resolver.Resolve(ctx, testTenantID, signIn("declared", "", false), closedPolicy)
		require.NoError(t, err)
		require.Equal(t, before, eventCount(t, db))
	})
}

// TestIdentityTakeoverOfPlatformAdmin replays the review of #124: a client
// clears then redeclares the identity of a default administrator, or changes
// its email, to sign in as that administrator.
func TestIdentityTakeoverOfPlatformAdmin(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newIdentityStore(t, db)
		svc := newIdentityService(store, service.WithReservedEmails("admin@corp.example"))
		resolver := service.NewIdentityResolver(store, store, store)
		policy := service.LoginPolicy{AutoCreate: true, ActiveByDefault: true, DefaultAdmins: []string{"admin@corp.example"}}

		// The default administrator signs in once.
		admin, err := resolver.Resolve(ctx, testTenantID, service.AuthenticatedIdentity{Provider: "oidc", Issuer: testIssuer, Subject: "admin", Email: "admin@corp.example", EmailVerified: true, DisplayName: "Admin"}, policy)
		require.NoError(t, err)
		require.Contains(t, admin.Roles(), model.PlatformRoleAdmin)
		require.Equal(t, []model.UserID{admin.ID()}, signInActors(t, db, admin.ID()), "a created account is its own actor")

		unchanged := func(identity service.IdentityChange) error {
			_, err := svc.PutTenantMember(ctx, testTenantID, admin.ID(), service.CommonMember{Email: "admin@corp.example", DisplayName: "Admin", TenantRole: model.TenantRoleMember, Status: model.StatusActive, Identity: identity})
			return err
		}
		require.NoError(t, unchanged(service.IdentityChange{}), "a PUT without identity changes nothing")
		require.ErrorIs(t, unchanged(service.IdentityChange{Kind: service.IdentityUnlink}), port.ErrPlatformAdminProtected)
		require.ErrorIs(t, unchanged(declare("attacker")), port.ErrPlatformAdminProtected)
		require.ErrorIs(t, putMember(svc, testTenantID, admin.ID(), "attacker@evil.example", service.IdentityChange{}), port.ErrPlatformAdminProtected)

		attacker, err := resolver.Resolve(ctx, testTenantID, signIn("attacker", "attacker@evil.example", true), policy)
		require.NoError(t, err)
		require.NotEqual(t, admin.ID(), attacker.ID())
		require.NotContains(t, attacker.Roles(), model.PlatformRoleAdmin)

		// Another identity proving the administrator's email is refused.
		_, err = resolver.Resolve(ctx, testTenantID, signIn("attacker-2", "admin@corp.example", true), policy)
		require.ErrorIs(t, err, service.ErrIdentityConflict)

		stored, err := store.GetUserByID(ctx, admin.ID())
		require.NoError(t, err)
		require.Equal(t, "admin@corp.example", stored.Email())
		require.Equal(t, "Admin", stored.DisplayName())
		require.True(t, stored.Active())
		require.Equal(t, "oidc", stored.Provider())
		require.Equal(t, "admin", stored.Subject())
		require.Nil(t, stored.DeclaredIdentity())
		require.ElementsMatch(t, []string{model.PlatformRoleUser, model.PlatformRoleAdmin}, stored.Roles())
	})
}

// TestIdentityUnlinkInvalidatesCache: once unlinked, a cached account no
// longer answers for its former sign-in.
func TestIdentityUnlinkInvalidatesCache(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newIdentityStore(t, db)
		cached := cache.NewUserStore(store, 100, time.Hour)
		transactions := cache.NewProvisioningTransaction(store, cached, nil)
		svc := service.NewProvisioningService(store, store, cached, store, service.WithProvisioningTransaction(transactions), service.WithProvisioningReader(store))
		resolver := service.NewIdentityResolver(cached, store, transactions)

		member := model.NewUserID()
		require.NoError(t, putMember(svc, testTenantID, member, "ivy@example.test", declare("ivy")))
		for range 2 {
			user, err := resolver.Resolve(ctx, testTenantID, signIn("ivy", "", false), closedPolicy)
			require.NoError(t, err)
			require.Equal(t, member, user.ID())
		}

		require.NoError(t, putMember(svc, testTenantID, member, "ivy@example.test", service.IdentityChange{Kind: service.IdentityUnlink}))
		_, err := resolver.Resolve(ctx, testTenantID, signIn("ivy", "", false), closedPolicy)
		require.ErrorIs(t, err, service.ErrAccountCreationDisabled)
		require.False(t, strings.Contains(err.Error(), string(member)))
	})
}
