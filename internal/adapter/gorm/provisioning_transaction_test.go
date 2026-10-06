package gorm_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/adapter/cache"
	"github.com/xolo-gateway/xolo/internal/adapter/events"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	gormpkg "gorm.io/gorm"
)

func provisioningService(store *xologorm.Store, tx port.ProvisioningTransaction) *service.ProvisioningService {
	return service.NewProvisioningService(store, store, store, store, service.WithProvisioningTransaction(tx), service.WithMultiTenant(true))
}

func provisionedTenant(t *testing.T, store *xologorm.Store) model.Tenant {
	t.Helper()
	tenant := model.NewTenant("provisioning-"+uuid.NewString(), "Provisioning", "")
	require.NoError(t, store.CreateTenant(t.Context(), tenant))
	return tenant
}

func provisionedOrg(t *testing.T, svc *service.ProvisioningService, tenant model.Tenant, subject string) *service.CreateOrganizationResult {
	t.Helper()
	result, err := svc.CreateOrganization(t.Context(), service.CreateOrganizationParams{TenantID: tenant.ID(), Slug: "org-" + uuid.NewString(), Name: "Organization", Owner: &service.UserIdentityParams{Provider: "oidc", Subject: subject}})
	require.NoError(t, err)
	return result
}

func TestProvisioningAtomicFailures(t *testing.T) {
	for _, stage := range []string{"users", "membership_roles", "mutation_audits"} {
		t.Run(stage, func(t *testing.T) {
			eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
				for _, existing := range []bool{false, true} {
					t.Run(map[bool]string{false: "new user", true: "existing user"}[existing], func(t *testing.T) {
						store := xologorm.NewStore(db)
						tenant := provisionedTenant(t, store)
						subject := uuid.NewString()
						var old model.User
						if existing {
							user, err := store.FindOrCreateUser(t.Context(), tenant.ID(), "oidc", subject)
							require.NoError(t, err)
							u := model.CopyUser(user)
							u.SetDisplayName("Original")
							u.SetRoles(model.PlatformRoleUser)
							require.NoError(t, store.SaveUser(t.Context(), u))
							old = u
						}
						cached := cache.NewUserStore(store, 100, time.Hour)
						if existing {
							_, err := cached.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", subject)
							require.NoError(t, err)
						}
						recorder := &invitationRecorder{}
						svc := provisioningService(store, events.NewProvisioningTransaction(cache.NewProvisioningTransaction(store, cached), recorder))
						failure := errors.New("injected mutation failure")
						require.NoError(t, db.Callback().Create().After("gorm:create").Register("test:provisioning_failure", func(tx *gormpkg.DB) {
							if tx.Statement.Table == stage {
								tx.AddError(failure)
							}
						}))
						result, err := svc.CreateOrganization(t.Context(), service.CreateOrganizationParams{TenantID: tenant.ID(), Slug: "failed", Name: "Failed", Owner: &service.UserIdentityParams{Provider: "oidc", Subject: subject, DisplayName: ptr("Uncommitted")}})
						require.NoError(t, db.Callback().Create().Remove("test:provisioning_failure"))
						require.ErrorIs(t, err, failure)
						require.Nil(t, result)
						_, err = store.GetOrgBySlug(t.Context(), tenant.ID(), "failed")
						require.ErrorIs(t, err, port.ErrNotFound)
						user, err := store.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", subject)
						if existing {
							require.NoError(t, err)
							require.Equal(t, old.DisplayName(), user.DisplayName())
							u, err := cached.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", subject)
							require.NoError(t, err)
							require.Equal(t, "Original", u.DisplayName())
						} else {
							require.ErrorIs(t, err, port.ErrNotFound)
						}
						for _, table := range []string{"organizations", "roles", "memberships", "membership_roles", "mutation_audits"} {
							var count int64
							require.NoError(t, db.Table(table).Count(&count).Error)
							require.Zero(t, count, table)
						}
						require.Empty(t, recorder.snapshot())
					})
				}
			})
		})
	}
}

func TestProvisioningAuditAndCascade(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := xologorm.NewStore(db)
		recorder := &invitationRecorder{}
		svc := provisioningService(store, events.NewProvisioningTransaction(store, recorder))
		ctx := model.WithActor(t.Context(), model.Actor{URI: "urn:test:provisioner", RequestID: strings.Repeat("a", 32)})
		tenant, err := svc.CreateTenant(ctx, service.CreateTenantParams{Slug: "audit", Name: "Audit"})
		require.NoError(t, err)
		result, err := svc.CreateOrganization(ctx, service.CreateOrganizationParams{TenantID: tenant.ID(), Slug: "audit", Name: "Audit", Owner: &service.UserIdentityParams{Provider: "oidc", Subject: "owner"}})
		require.NoError(t, err)
		var audits []xologorm.MutationAudit
		require.NoError(t, db.Order("resource, resource_id").Find(&audits).Error)
		require.Len(t, audits, 7) // tenant, organization, user, membership and three builtin roles
		for _, audit := range audits {
			_, err := uuid.Parse(audit.ID)
			require.NoError(t, err)
			require.Equal(t, string(tenant.ID()), audit.TenantID)
			require.Equal(t, "null", audit.Before)
			require.NotEqual(t, "null", audit.After)
			require.NotContains(t, audit.After, "created_at")
			require.NotContains(t, audit.After, "updated_at")
			require.NotContains(t, audit.After, "auth_token")
			require.NotContains(t, audit.After, "preferences")
			require.Contains(t, audit.Actor, "urn:test:provisioner")
			require.Equal(t, strings.Repeat("a", 32), audit.RequestID)
			if audit.Resource == "membership" {
				require.Contains(t, audit.After, string(result.OwnerMembership.Roles()[0].ID()))
			}
		}
		emitted := recorder.snapshot()
		require.Len(t, emitted, 2)
		require.Equal(t, model.EventTypeMemberAdded, emitted[0].Type())
		require.Equal(t, model.EventTypeMemberUpdated, emitted[1].Type())
		for _, event := range emitted {
			require.Equal(t, "urn:test:provisioner", event.Attributes()["actor_uri"])
			require.Equal(t, strings.Repeat("a", 32), event.Attributes()["request_id"])
		}
		_, err = svc.UpdateOrganization(ctx, tenant.ID(), result.Org.ID(), service.UpdateOrganizationParams{Name: ptr("Audit")})
		require.NoError(t, err)
		_, err = svc.SetMemberRoles(ctx, tenant.ID(), result.Org.ID(), result.OwnerMembership.ID(), nil, []string{"owner"})
		require.NoError(t, err)
		var count int64
		require.NoError(t, db.Model(&xologorm.MutationAudit{}).Count(&count).Error)
		require.EqualValues(t, 7, count)
		require.NoError(t, svc.DeleteTenant(ctx, tenant.ID()))
		var deleted []xologorm.MutationAudit
		require.NoError(t, db.Where("after = ?", "null").Find(&deleted).Error)
		require.Len(t, deleted, 7)
		for _, audit := range deleted {
			require.Equal(t, string(tenant.ID()), audit.TenantID)
			require.NotEqual(t, "null", audit.Before)
		}
		require.NoError(t, db.Model(&xologorm.MutationAudit{}).Count(&count).Error)
		require.EqualValues(t, 14, count)
	})
}

// retryProvisioning wraps the callback inside the real database transaction.
// Returning a serialization error after it succeeds forces a complete rollback.
type retryProvisioning struct {
	port.ProvisioningTransaction
	attempts atomic.Int32
	after    func()
}

func (r *retryProvisioning) WithProvisioningTransaction(ctx context.Context, fn func(port.ProvisioningTx) error) error {
	return r.ProvisioningTransaction.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if r.attempts.Add(1) == 1 {
			if r.after != nil {
				r.after()
			}
			return &pgconn.PgError{Code: "40001"}
		}
		return nil
	})
}

func TestProvisioningRetryEffectsAndCancellation(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := xologorm.NewStore(db)
		tenant := provisionedTenant(t, store)
		recorder := &invitationRecorder{}
		replay := &retryProvisioning{ProvisioningTransaction: store}
		svc := provisioningService(store, events.NewProvisioningTransaction(replay, recorder))
		result := provisionedOrg(t, svc, tenant, "retry")
		require.EqualValues(t, 2, replay.attempts.Load())
		require.Len(t, recorder.snapshot(), 2)
		var audits []xologorm.MutationAudit
		require.NoError(t, db.Find(&audits).Error)
		require.Len(t, audits, 6)
		requestID := audits[0].RequestID
		require.Len(t, requestID, 32)
		for _, audit := range audits {
			require.Equal(t, requestID, audit.RequestID)
			require.Contains(t, audit.Actor, "urn:xolo:operator:local")
		}
		require.Equal(t, requestID, recorder.snapshot()[0].Attributes()["request_id"])
		require.Equal(t, string(result.Owner.ID()), recorder.snapshot()[0].Attributes()["member_user_id"])
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		replay = &retryProvisioning{ProvisioningTransaction: store, after: cancel}
		svc = provisioningService(store, events.NewProvisioningTransaction(replay, recorder))
		user, created, err := svc.ProvisionUser(ctx, tenant.ID(), service.UserIdentityParams{Provider: "oidc", Subject: "canceled"})
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, user)
		require.False(t, created)
		_, err = store.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", "canceled")
		require.ErrorIs(t, err, port.ErrNotFound)
		require.Len(t, recorder.snapshot(), 2)
	})
}

type synchronizedProvisioning struct {
	port.ProvisioningTransaction
	identity bool
	reads    atomic.Int32
	release  chan struct{}
}

func (s *synchronizedProvisioning) WithProvisioningTransaction(ctx context.Context, fn func(port.ProvisioningTx) error) error {
	return s.ProvisioningTransaction.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		return fn(&synchronizedProvisioningTx{ProvisioningTx: tx, sync: s})
	})
}
func (s *synchronizedProvisioning) wait(ctx context.Context) error {
	n := s.reads.Add(1)
	if n == 2 {
		close(s.release)
	}
	if n > 2 {
		return nil
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type synchronizedProvisioningTx struct {
	port.ProvisioningTx
	sync *synchronizedProvisioning
}

func (tx *synchronizedProvisioningTx) GetUserByIdentity(ctx context.Context, tenant model.TenantID, provider, subject string) (model.User, error) {
	u, err := tx.ProvisioningTx.GetUserByIdentity(ctx, tenant, provider, subject)
	if tx.sync.identity {
		if waitErr := tx.sync.wait(ctx); waitErr != nil {
			return nil, waitErr
		}
	}
	return u, err
}
func (tx *synchronizedProvisioningTx) ListOrgMembers(ctx context.Context, org model.OrgID, opts port.ListOrgMembersOptions) ([]model.Membership, int64, error) {
	members, count, err := tx.ProvisioningTx.ListOrgMembers(ctx, org, opts)
	if !tx.sync.identity {
		if waitErr := tx.sync.wait(ctx); waitErr != nil {
			return nil, 0, waitErr
		}
	}
	return members, count, err
}

func TestProvisioningConcurrentIdentityAndOwners(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		tenant := provisionedTenant(t, store)
		barrier := &synchronizedProvisioning{ProvisioningTransaction: store, identity: true, release: make(chan struct{})}
		svc := provisioningService(store, barrier)
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		results := make(chan model.User, 2)
		errs := make(chan error, 2)
		for range 2 {
			wg.Go(func() {
				u, _, err := svc.ProvisionUser(ctx, tenant.ID(), service.UserIdentityParams{Provider: "oidc", Subject: "same"})
				results <- u
				errs <- err
			})
		}
		wg.Wait()
		require.NoError(t, <-errs)
		require.NoError(t, <-errs)
		require.Equal(t, (<-results).ID(), (<-results).ID())
		svc = provisioningService(store, store)
		org := provisionedOrg(t, svc, tenant, "owner1")
		second, err := svc.AddMember(ctx, tenant.ID(), org.Org.ID(), service.AddMemberParams{User: &service.UserIdentityParams{Provider: "oidc", Subject: "owner2"}, BuiltinRoles: []string{"owner"}})
		require.NoError(t, err)
		barrier = &synchronizedProvisioning{ProvisioningTransaction: store, release: make(chan struct{})}
		svc = provisioningService(store, barrier)
		for _, id := range []model.MembershipID{org.OwnerMembership.ID(), second.ID()} {
			wg.Go(func() {
				_, err := svc.SetMemberRoles(ctx, tenant.ID(), org.Org.ID(), id, nil, []string{"member"})
				errs <- err
			})
		}
		wg.Wait()
		firstErr, secondErr := <-errs, <-errs
		if firstErr == nil {
			require.ErrorIs(t, secondErr, port.ErrNotAllowed)
		} else {
			require.ErrorIs(t, firstErr, port.ErrNotAllowed)
			require.NoError(t, secondErr)
		}
	})
}

func TestProvisioningIndependentTenants(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		if db.Dialector.Name() != "postgres" {
			t.Skip("PostgreSQL concurrent writers")
		}
		store := xologorm.NewStore(db)
		first := provisionedTenant(t, store)
		second := provisionedTenant(t, store)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		err := store.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
			if err := tx.SaveTenant(ctx, model.UpdateTenant(first, model.WithTenantName("Held"))); err != nil {
				return err
			}
			_, err := provisioningService(store, store).UpdateTenant(ctx, second.ID(), service.UpdateTenantParams{Name: ptr("Independent")})
			return err
		})
		require.NoError(t, err)
	})
}

func TestProvisioningCacheAfterCommit(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := xologorm.NewStore(db)
		tenant := provisionedTenant(t, store)
		cached := cache.NewUserStore(store, 100, time.Hour)
		svc := provisioningService(store, cache.NewProvisioningTransaction(store, cached))
		org := provisionedOrg(t, svc, tenant, "cached")
		u, err := cached.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", "cached")
		require.NoError(t, err)
		token := model.NewAuthToken(u, org.Org.ID(), "cached", "cache-test-token", nil)
		require.NoError(t, store.CreateAuthToken(t.Context(), token))
		_, err = cached.FindAuthToken(t.Context(), token.Value())
		require.NoError(t, err)
		failure := errors.New("rollback")
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:cache_rollback", func(tx *gormpkg.DB) {
			if tx.Statement.Table == "mutation_audits" {
				observed, readErr := cached.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", "cached")
				require.NoError(t, readErr)
				require.Equal(t, u.DisplayName(), observed.DisplayName())
				tx.AddError(failure)
			}
		}))
		_, err = svc.UpdateUser(t.Context(), tenant.ID(), u.ID(), service.UpdateUserParams{DisplayName: ptr("provisional")})
		require.ErrorIs(t, err, failure)
		require.NoError(t, db.Callback().Create().Remove("test:cache_rollback"))
		_, err = svc.UpdateUser(t.Context(), tenant.ID(), u.ID(), service.UpdateUserParams{DisplayName: ptr("committed")})
		require.NoError(t, err)
		for _, read := range []func() (model.User, error){func() (model.User, error) { return cached.GetUserByID(t.Context(), u.ID()) }, func() (model.User, error) {
			return cached.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", "cached")
		}} {
			actual, err := read()
			require.NoError(t, err)
			require.Equal(t, "committed", actual.DisplayName())
		}
		actualToken, err := cached.FindAuthToken(t.Context(), token.Value())
		require.NoError(t, err)
		require.Equal(t, "committed", actualToken.Owner().DisplayName())
		require.NoError(t, svc.DeleteTenant(t.Context(), tenant.ID()))
		_, err = cached.GetUserByID(t.Context(), u.ID())
		require.ErrorIs(t, err, port.ErrNotFound)
		_, err = cached.GetUserByIdentity(t.Context(), tenant.ID(), "oidc", "cached")
		require.ErrorIs(t, err, port.ErrNotFound)
		_, err = cached.FindAuthToken(t.Context(), token.Value())
		require.ErrorIs(t, err, port.ErrNotFound)
	})
}

func TestProvisioningRequiresAdapterAndParents(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := xologorm.NewStore(db)
		tenant := provisionedTenant(t, store)
		unbound := service.NewProvisioningService(store, store, store, store)
		_, _, err := unbound.ProvisionUser(t.Context(), tenant.ID(), service.UserIdentityParams{Provider: "oidc", Subject: "missing-adapter"})
		require.ErrorContains(t, err, "transaction adapter")
		svc := provisioningService(store, store)
		org := provisionedOrg(t, svc, tenant, "owner")
		otherTenant := provisionedTenant(t, store)
		otherOrg := provisionedOrg(t, svc, otherTenant, "other")
		_, err = svc.CreateRole(t.Context(), otherTenant.ID(), org.Org.ID(), service.RoleParams{Name: ptr("foreign")})
		require.ErrorIs(t, err, port.ErrNotFound)
		_, err = svc.AddMember(t.Context(), otherTenant.ID(), org.Org.ID(), service.AddMemberParams{UserID: otherOrg.Owner.ID()})
		require.ErrorIs(t, err, port.ErrNotFound)
		_, err = svc.AddMember(t.Context(), tenant.ID(), org.Org.ID(), service.AddMemberParams{UserID: otherOrg.Owner.ID()})
		require.ErrorIs(t, err, port.ErrNotFound)
		_, err = svc.SetMemberRoles(t.Context(), tenant.ID(), org.Org.ID(), org.OwnerMembership.ID(), []model.RoleID{otherOrg.OwnerMembership.Roles()[0].ID()}, nil)
		require.ErrorIs(t, err, port.ErrInvalid)
		_, err = svc.UpdateUser(t.Context(), otherTenant.ID(), org.Owner.ID(), service.UpdateUserParams{Active: ptr(false)})
		require.ErrorIs(t, err, port.ErrNotFound)
		defaultTenant, err := store.GetTenantBySlug(t.Context(), model.DefaultTenantSlug)
		require.NoError(t, err)
		require.ErrorIs(t, svc.DeleteTenant(t.Context(), defaultTenant.ID()), port.ErrNotAllowed)
		_, err = svc.UpdateTenant(t.Context(), defaultTenant.ID(), service.UpdateTenantParams{Active: ptr(false)})
		require.ErrorIs(t, err, port.ErrNotAllowed)
		require.Equal(t, []string{model.PlatformRoleUser}, org.Owner.Roles())
		_, _, err = svc.ProvisionUser(t.Context(), model.NewTenantID(), service.UserIdentityParams{Provider: "oidc", Subject: "orphan"})
		require.ErrorIs(t, err, port.ErrNotFound)
	})
}

func TestProvisioningAuditMigration(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := xologorm.NewStore(db)
		require.NoError(t, store.Migrate(t.Context()))
		require.True(t, db.Migrator().HasTable(&xologorm.MutationAudit{}))
		// Reconstruct PR 125's schema, retaining its UUID rows and recovery artifacts.
		require.NoError(t, db.Migrator().DropTable(&xologorm.MutationAudit{}))
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610060001").Error)
		tenant := provisionedTenant(t, store)
		disabled := xologorm.NewStore(db, xologorm.WithAutoMigrate(false))
		require.Error(t, disabled.CheckSchema(t.Context()))
		require.NoError(t, xologorm.NewStore(db).Migrate(t.Context()))
		require.NoError(t, disabled.CheckSchema(t.Context()))
		svc := provisioningService(disabled, disabled)
		_, err := svc.UpdateTenant(t.Context(), tenant.ID(), service.UpdateTenantParams{Name: ptr("upgraded")})
		require.NoError(t, err)
		var before, after []xologorm.MutationAudit
		require.NoError(t, db.Find(&before).Error)
		require.Len(t, before, 1)
		require.NoError(t, xologorm.NewStore(db).Migrate(t.Context()))
		require.NoError(t, db.Find(&after).Error)
		require.Equal(t, before, after)
		// Ordinary writes continue to work without producing provisioning audit entries.
		require.NoError(t, store.SaveTenant(t.Context(), model.UpdateTenant(tenant, model.WithTenantName("ordinary"))))
		var count int64
		require.NoError(t, db.Model(&xologorm.MutationAudit{}).Count(&count).Error)
		require.EqualValues(t, 1, count)
	})
}

// Fail the first physical commit after rolling back its writes, so the test
// exercises errors returned by Commit rather than only callback failures.
type provisioningCommitDB struct {
	*sql.DB
	commits *atomic.Int32
}

func (db provisioningCommitDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (gormpkg.ConnPool, error) {
	tx, err := db.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &provisioningCommitTx{Tx: tx, commits: db.commits}, nil
}

type provisioningCommitTx struct {
	*sql.Tx
	commits *atomic.Int32
}

func (tx provisioningCommitTx) Commit() error {
	if tx.commits.Add(1) == 1 {
		if err := tx.Tx.Rollback(); err != nil {
			return err
		}
		return &pgconn.PgError{Code: "40001", Message: "injected commit failure"}
	}
	return tx.Tx.Commit()
}
func TestProvisioningCommitRetry(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		original := xologorm.NewStore(db)
		tenant := provisionedTenant(t, original)
		pool, err := db.DB()
		require.NoError(t, err)
		commits := &atomic.Int32{}
		wrapped := db.WithContext(t.Context())
		wrapped.Statement.ConnPool = provisioningCommitDB{DB: pool, commits: commits}
		store := xologorm.NewStore(wrapped, xologorm.WithAutoMigrate(false))
		recorder := &invitationRecorder{}
		svc := provisioningService(store, events.NewProvisioningTransaction(store, recorder))
		result := provisionedOrg(t, svc, tenant, "commit-retry")
		require.EqualValues(t, 2, commits.Load())
		require.Len(t, recorder.snapshot(), 2)
		var audits []xologorm.MutationAudit
		require.NoError(t, db.Find(&audits).Error)
		require.Len(t, audits, 6)
		for _, audit := range audits {
			if audit.Resource == "organization" {
				require.Equal(t, string(result.Org.ID()), audit.ResourceID)
			}
		}
	})
}
