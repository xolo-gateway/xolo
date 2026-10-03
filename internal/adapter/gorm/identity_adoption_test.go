package gorm_test

import (
	"context"
	"encoding/json"
	"errors"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/admin"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/adoption"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"gorm.io/gorm"
)

func TestDeclaredIdentityAndLogin(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		ctx := t.Context()
		s := adapter.NewStore(db)
		require.NoError(t, s.Migrate(ctx))
		api := service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))
		tenant, err := s.GetTenantBySlug(ctx, model.DefaultTenantSlug)
		require.NoError(t, err)
		tid := tenant.ID()
		uid := model.NewUserID()
		identity := &model.Identity{Issuer: "https://id.example/", Subject: "Subject "}
		p := service.CommonMember{Email: "contact@example.test", DisplayName: "Managed", TenantRole: "owner", Status: "active", Identity: identity}
		_, err = api.PutCommonMember(ctx, tid, uid, p)
		require.NoError(t, err)
		read := func() model.CommonItem {
			item, err := s.ReadCommon(ctx, model.CommonScope{Family: "member", TenantID: string(tid)}, string(uid))
			require.NoError(t, err)
			return item
		}
		first := read()
		require.Contains(t, string(first.Representation), `"identity"`)
		proof := service.AuthenticatedIdentity{Provider: "idp", Issuer: identity.Issuer, Subject: identity.Subject, Email: "unverified@example.test", DisplayName: "IdP Name"}
		login := func(proof service.AuthenticatedIdentity) (model.User, error) {
			return service.ResolveAuthenticatedIdentity(ctx, s, tid, proof, service.LoginPolicy{Managed: true})
		}
		user, err := login(proof)
		require.NoError(t, err)
		require.Equal(t, uid, user.ID())
		require.Equal(t, p.Email, user.Email())
		require.Equal(t, first, read())
		var audits int64
		require.NoError(t, db.Model(&adapter.MutationAudit{}).Count(&audits).Error)
		_, err = login(proof)
		require.NoError(t, err)
		var after int64
		require.NoError(t, db.Model(&adapter.MutationAudit{}).Count(&after).Error)
		require.Equal(t, audits, after)
		p.Email = "changed@example.test"
		_, err = api.PutCommonMember(ctx, tid, uid, p)
		require.NoError(t, err)
		user, err = s.GetUserByID(ctx, uid)
		require.NoError(t, err)
		require.Equal(t, identity.Issuer, user.Provider())
		before := read()
		_, err = api.PutCommonMember(ctx, tid, uid, p)
		require.NoError(t, err)
		require.Equal(t, before, read())
		conflicting := p
		conflicting.Identity = &model.Identity{Issuer: identity.Issuer, Subject: "someone-else"}
		_, err = api.PutCommonMember(ctx, tid, uid, conflicting)
		require.ErrorIs(t, err, port.ErrAlreadyExists)
		require.Equal(t, before, read())
		_, err = api.PutCommonMember(ctx, tid, model.NewUserID(), p)
		require.ErrorIs(t, err, port.ErrAlreadyExists)
		p.Identity = nil
		_, err = api.PutCommonMember(ctx, tid, uid, p)
		require.NoError(t, err)
		user, err = s.GetUserByID(ctx, uid)
		require.NoError(t, err)
		require.Empty(t, user.Provider())
		require.Nil(t, user.DeclaredIdentity())
		proof.Email = p.Email
		_, err = login(proof)
		require.ErrorIs(t, err, port.ErrNotAllowed)
		proof.EmailVerified = true
		user, err = login(proof)
		require.NoError(t, err)
		require.Equal(t, uid, user.ID())
		proof.Subject = "other"
		_, err = login(proof)
		require.ErrorIs(t, err, port.ErrAlreadyExists)
		// Identity values never appear in public events.
		var publications []adapter.Publication
		require.NoError(t, db.Find(&publications).Error)
		for _, event := range publications {
			require.NotContains(t, event.Payload, identity.Issuer)
			require.NotContains(t, event.Payload, identity.Subject)
		}
	})
}

func TestIdentityRollbackAndTenantIsolation(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		ctx := t.Context()
		s := adapter.NewStore(db)
		require.NoError(t, s.Migrate(ctx))
		api := service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))
		identity := &model.Identity{Issuer: "https://id.example", Subject: "one"}
		p := service.CommonMember{Email: "one@example.test", TenantRole: "member", Status: "active", Identity: identity}
		var users []model.UserID
		for _, slug := range []string{"first", "second"} {
			tid := model.NewTenantID()
			_, err := api.PutCommonTenant(ctx, tid, service.CommonResource{Slug: slug, Name: slug, Status: "active"})
			require.NoError(t, err)
			uid := model.NewUserID()
			_, err = api.PutCommonMember(ctx, tid, uid, p)
			require.NoError(t, err)
			u, err := service.ResolveAuthenticatedIdentity(ctx, s, tid, service.AuthenticatedIdentity{Provider: "idp", Issuer: identity.Issuer, Subject: identity.Subject}, service.LoginPolicy{Managed: true})
			require.NoError(t, err)
			require.Equal(t, uid, u.ID())
			users = append(users, u.ID())
		}
		require.NotEqual(t, users[0], users[1])
		u, err := s.GetUserByID(ctx, users[0])
		require.NoError(t, err)
		sentinel := errors.New("rollback")
		err = s.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
			copy := model.CopyUser(u)
			copy.SetIdentity("", "")
			copy.SetDeclaredIdentity(nil)
			require.NoError(t, tx.SaveUser(ctx, copy))
			return sentinel
		})
		require.ErrorIs(t, err, sentinel)
		u, err = s.GetUserByID(ctx, users[0])
		require.NoError(t, err)
		require.Equal(t, identity, u.DeclaredIdentity())
		require.Equal(t, identity.Issuer, u.Provider())
	})
}

func TestDurableLogoutAndRollback(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		ctx := t.Context()
		s := adapter.NewStore(db)
		require.NoError(t, s.Migrate(ctx))
		start := time.Now().Add(-time.Minute)
		open := func(subject string) string {
			id, err := s.OpenSession(ctx, "https://issuer", subject, start, time.Now().Add(time.Hour))
			require.NoError(t, err)
			return id
		}
		a, b, other := open("one"), open("one"), open("two")
		require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("logout-failure", func(tx *gorm.DB) {
			if tx.Statement.Table == "identity_sessions" {
				tx.AddError(errors.New("injected"))
			}
		}))
		require.Error(t, s.RevokeIdentitySessions(ctx, "https://issuer", "one", "jti", time.Now()))
		require.NoError(t, db.Callback().Delete().Remove("logout-failure"))
		require.NoError(t, s.CheckSession(ctx, a, "https://issuer", "one"))
		require.NoError(t, s.RevokeIdentitySessions(ctx, "https://issuer", "one", "jti", time.Now()))
		reopened := adapter.NewStore(db)
		for _, id := range []string{a, b} {
			require.ErrorIs(t, reopened.CheckSession(ctx, id, "https://issuer", "one"), port.ErrNotAllowed)
		}
		require.NoError(t, reopened.CheckSession(ctx, other, "https://issuer", "two"))
		require.ErrorIs(t, reopened.RevokeIdentitySessions(ctx, "https://issuer", "one", "jti", time.Now()), port.ErrAlreadyExists)
		_, err := reopened.OpenSession(ctx, "https://issuer", "one", start, time.Now().Add(time.Hour))
		require.ErrorIs(t, err, port.ErrNotAllowed)
		_, err = reopened.OpenSession(ctx, "https://issuer", "one", time.Now().Add(time.Millisecond), time.Now().Add(time.Hour))
		require.NoError(t, err)
	})
}

func TestAdoptionOwnershipRoundTrip(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		ctx := t.Context()
		s := adapter.NewStore(db)
		require.NoError(t, s.Migrate(ctx))
		require.NoError(t, s.ConfigureOwnership(nil))
		tenant, err := s.GetTenantBySlug(ctx, model.DefaultTenantSlug)
		require.NoError(t, err)
		admin := model.NewUser(tenant.ID(), "idp", "operator", "operator@example.test", "Operator", true, model.PlatformRoleAdmin)
		require.NoError(t, s.SaveUser(ctx, admin))
		scope := model.CommonScope{Family: "member", TenantID: string(tenant.ID())}
		before, err := s.ReadCommon(ctx, scope, string(admin.ID()))
		require.NoError(t, err)
		raw, err := s.ExportAdoption(ctx)
		require.NoError(t, err)
		payload, err := adoption.Decode(raw)
		require.NoError(t, err)
		require.GreaterOrEqual(t, payload.Count, 2)
		_, err = adoption.Decode(raw[:len(raw)-1])
		require.Error(t, err)
		_, err = adoption.Decode([]byte(strings.Replace(string(raw), "Operator", "Tampered", 1)))
		require.Error(t, err)
		cp := model.WithWriteAuthority(ctx, model.OwnerControlPlane)
		require.ErrorIs(t, s.SaveUser(cp, admin), port.ErrNotAllowed)
		require.NoError(t, s.ConfigureOwnership(model.OwnershipPolicy{"member": model.OwnerControlPlane, "subscription": model.OwnerControlPlane}))
		require.ErrorIs(t, s.SaveUser(ctx, admin), port.ErrNotAllowed)
		require.NoError(t, s.SaveUser(cp, admin))
		_, err = s.PutWebhook(cp, string(tenant.ID()), string(model.NewUserID()), model.WebhookSettings{Destination: "https://console.test", Events: []string{"member.updated.v1"}, Enabled: true, EncryptedSecrets: "encrypted", SecretCount: 1})
		require.NoError(t, err)
		operator := model.WithActor(ctx, model.Actor{URI: "urn:xolo:operator:test"})
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register("detach-failure", func(tx *gorm.DB) {
			if tx.Statement.Table == "mutation_audits" {
				tx.AddError(errors.New("injected detach failure"))
			}
		}))
		require.Error(t, s.DetachControlPlane(operator))
		require.NoError(t, db.Callback().Create().Remove("detach-failure"))
		preserved, err := s.ListWebhooks(ctx, string(tenant.ID()))
		require.NoError(t, err)
		require.Len(t, preserved, 1)
		require.NoError(t, s.DetachControlPlane(operator))
		require.NoError(t, s.ConfigureOwnership(nil))
		require.NoError(t, s.SaveUser(ctx, admin))
		after, err := s.ReadCommon(ctx, scope, string(admin.ID()))
		require.NoError(t, err)
		require.Equal(t, before, after)
		raw, err = s.ExportAdoption(ctx)
		require.NoError(t, err)
		final, err := adoption.Decode(raw)
		require.NoError(t, err)
		require.Equal(t, payload.Source, final.Source)
		_, err = s.ReadCommonEvents(ctx, payload.Cursor, 1000)
		require.NoError(t, err)
		subscriptions, err := s.ListWebhooks(ctx, string(tenant.ID()))
		require.NoError(t, err)
		require.Empty(t, subscriptions)
		var rep map[string]any
		require.NoError(t, json.Unmarshal(before.Representation, &rep))
		require.Equal(t, "operator@example.test", rep["email"])
	})
}

// A paused transaction holds the same lock used by session issuance and logout;
// another connection cannot resurrect a login whose authentication began earlier.
func TestConcurrentLoginLogout(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		ctx := t.Context()
		s := adapter.NewStore(db)
		require.NoError(t, s.Migrate(ctx))
		start := time.Now().Add(-time.Second)
		locked := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- s.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
				close(locked)
				<-release
				_, err := tx.(port.SessionRegistry).OpenSession(ctx, "https://issuer", "subject", start, time.Now().Add(time.Hour))
				return err
			})
		}()
		<-locked
		revoked := make(chan error, 1)
		go func() {
			revoked <- s.RevokeIdentitySessions(context.Background(), "https://issuer", "subject", "race", time.Now())
		}()
		close(release)
		require.NoError(t, <-done)
		require.NoError(t, <-revoked)
		var count int64
		require.NoError(t, db.Model(&adapter.IdentitySession{}).Count(&count).Error)
		require.Zero(t, count)
		_, err := s.OpenSession(ctx, "https://issuer", "subject", start, time.Now().Add(time.Hour))
		require.ErrorIs(t, err, port.ErrNotAllowed)
	})
}

func TestIdentityAliasesAndBootstrap(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		ctx := t.Context()
		s := adapter.NewStore(db)
		require.NoError(t, s.Migrate(ctx))
		s.ConfigureIdentityProviders(map[string]string{"legacy": "https://issuer.test", "second": "https://issuer.test"})
		tenant, err := s.GetTenantBySlug(ctx, "default")
		require.NoError(t, err)
		old := model.NewUser(tenant.ID(), "legacy", "subject", "old@example.test", "Old", true)
		require.NoError(t, s.SaveUser(ctx, old))
		api := service.NewProvisioningService(s, s, s, s)
		p := service.CommonMember{Email: old.Email(), DisplayName: old.DisplayName(), TenantRole: "member", Status: "active", Identity: &model.Identity{Issuer: "https://issuer.test", Subject: "subject"}}
		_, err = api.PutCommonMember(ctx, tenant.ID(), old.ID(), p)
		require.NoError(t, err)
		linked, err := s.GetUserByID(ctx, old.ID())
		require.NoError(t, err)
		require.Equal(t, "legacy", linked.Provider())
		other := model.NewUser(tenant.ID(), "second", "subject", "different@test", "Other", true)
		require.ErrorIs(t, s.SaveUser(ctx, other), port.ErrAlreadyExists)
		proof := service.AuthenticatedIdentity{Provider: "second", Issuer: "https://issuer.test", Subject: "subject"}
		linked, err = service.ResolveAuthenticatedIdentity(ctx, s, tenant.ID(), proof, service.LoginPolicy{Managed: true})
		require.NoError(t, err)
		require.Equal(t, old.ID(), linked.ID())
		policy := service.LoginPolicy{Managed: true, DefaultAdmins: []string{"admin@test"}}
		proof.Subject = "admin"
		proof.Email = "admin@test"
		_, err = service.ResolveAuthenticatedIdentity(ctx, s, tenant.ID(), proof, policy)
		require.ErrorIs(t, err, port.ErrNotAllowed)
		proof.EmailVerified = true
		admin, err := service.ResolveAuthenticatedIdentity(ctx, s, tenant.ID(), proof, policy)
		require.NoError(t, err)
		require.Contains(t, admin.Roles(), model.PlatformRoleAdmin)
	})
}

func TestManagedAdminWritersAndFragments(t *testing.T) {
	eachBackend(t, func(t *testing.T, s *adapter.Store) {
		ctx := t.Context()
		tenant, err := s.GetTenantBySlug(ctx, "default")
		require.NoError(t, err)
		operator := model.NewUser(tenant.ID(), "local", "operator", "operator@test", "Operator", true, model.PlatformRoleAdmin)
		require.NoError(t, s.SaveUser(ctx, operator))
		target := model.NewUser(tenant.ID(), "local", "target", "target@test", "Target", true)
		require.NoError(t, s.SaveUser(ctx, target))
		org := model.NewOrganization(tenant.ID(), "managed", "Managed", "")
		require.NoError(t, s.CreateOrg(ctx, org))
		require.NoError(t, s.ConfigureOwnership(model.OwnershipPolicy{"member": "control_plane", "organization": "control_plane"}))
		handler := admin.NewHandler(s, s, s, s, nil, nil, nil)
		for _, fragment := range []bool{false, true} {
			for _, op := range []struct{ method, path, body string }{
				{"DELETE", "/users/" + string(target.ID()), ""},
				{"POST", "/orgs/" + string(org.ID()) + "/edit", "name=Changed&active=on"},
			} {
				r := httptest.NewRequest(op.method, op.path, strings.NewReader(op.body))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if fragment {
					r.Header.Set("HX-Request", "true")
				}
				rctx := httpCtx.SetTenant(r.Context(), tenant)
				rctx = httpCtx.SetUser(rctx, operator)
				rctx = httpCtx.SetBaseURL(rctx, "https://gateway.test")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r.WithContext(rctx))
				require.Equal(t, 403, w.Code, w.Body.String())
			}
		}
		stored, err := s.GetUserByID(ctx, target.ID())
		require.NoError(t, err)
		require.Equal(t, target.Email(), stored.Email())
		storedOrg, err := s.GetOrgByID(ctx, org.ID())
		require.NoError(t, err)
		require.Equal(t, "Managed", storedOrg.Name())
	})
}

func TestOwnershipBlocksForeignFamilyCascade(t *testing.T) {
	eachBackend(t, func(t *testing.T, s *adapter.Store) {
		ctx := t.Context()
		tenant := model.NewTenant("cascade", "Cascade", "")
		require.NoError(t, s.CreateTenant(ctx, tenant))
		u := model.NewUser(tenant.ID(), "local", "subject", "u@test", "User", true)
		require.NoError(t, s.SaveUser(ctx, u))
		require.NoError(t, s.ConfigureOwnership(model.OwnershipPolicy{"member": "control_plane"}))
		s.ConfigureLifecycle(true, time.Second)
		require.ErrorIs(t, s.DeleteTenant(ctx, tenant.ID()), port.ErrOwnershipDenied)
		_, err := s.GetTenantByID(ctx, tenant.ID())
		require.NoError(t, err)
		_, err = s.GetUserByID(ctx, u.ID())
		require.NoError(t, err)
	})
}
