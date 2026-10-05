package gorm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rs/xid"
	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/crypto"
	gormpkg "gorm.io/gorm"
)

func TestCommonUnlinkedMembers(t *testing.T) {
	eachBackend(t, func(t *testing.T, s *adapter.Store) {
		ctx := t.Context()
		a := model.NewUser(testTenantID, "", "", " A@example.test ", "A", true)
		b := model.NewUser(testTenantID, "", "", "b@example.test", "B", true)
		require.NoError(t, s.SaveUser(ctx, a))
		require.NoError(t, s.SaveUser(ctx, b))
		got, err := s.GetUserByID(ctx, a.ID())
		require.NoError(t, err)
		require.Equal(t, "a@example.test", got.Email())
		a.SetIdentity("oidc", "subject")
		require.NoError(t, s.SaveUser(ctx, a))
		b.SetIdentity("oidc", "subject")
		require.Error(t, s.SaveUser(ctx, b))
		b.SetIdentity("", "")
		b.SetEmail("A@EXAMPLE.TEST")
		require.ErrorIs(t, s.SaveUser(ctx, b), port.ErrAlreadyExists)
	})
}
func TestCommonAtomicFacts(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		s := adapter.NewStore(db)
		ctx := model.WithActor(t.Context(), model.Actor{URI: "urn:test:console", RequestID: "request-1"})
		require.NoError(t, s.Migrate(ctx))
		tenant := model.NewTenant("facts", "Facts", "")
		require.NoError(t, s.CreateTenant(ctx, tenant))
		count := func(table string) int64 { var n int64; require.NoError(t, db.Table(table).Count(&n).Error); return n }
		initial := count("mutation_audits")
		require.Equal(t, int64(1), initial)
		require.NoError(t, s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName(tenant.Name()))))
		require.Equal(t, initial, count("mutation_audits"))
		for _, table := range []string{"organizations", "roles", "role_permissions", "users", "memberships", "membership_roles", "events", "mutation_audits", "publications"} {
			t.Run(table, func(t *testing.T) {
				sentinel := errors.New("injected write failure")
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("fail_common", func(tx *gormpkg.DB) {
					if tx.Statement.Table == table {
						tx.AddError(sentinel)
					}
				}))
				defer db.Callback().Create().Remove("fail_common")
				svc := service.NewProvisioningService(s, s, s, s)
				_, err := svc.CreateOrganization(ctx, service.CreateOrganizationParams{TenantID: tenant.ID(), Slug: "rollback", Name: "Rollback", Owner: &service.UserIdentityParams{Provider: "test", Subject: "owner"}})
				require.ErrorIs(t, err, sentinel)
				_, err = s.GetOrgBySlug(ctx, tenant.ID(), "rollback")
				require.ErrorIs(t, err, port.ErrNotFound)
				_, err = s.GetUserByIdentity(ctx, tenant.ID(), "test", "owner")
				require.ErrorIs(t, err, port.ErrNotFound)
				require.Equal(t, initial, count("mutation_audits"))
				require.Equal(t, initial, count("publications"))
			})
		}
		var audit adapter.MutationAudit
		require.NoError(t, db.First(&audit).Error)
		require.Contains(t, audit.Actor, "urn:test:console")
		require.Equal(t, "request-1", audit.RequestID)
	})
}
func TestCommonRolePreservesCustomAndParentStatus(t *testing.T) {
	eachBackend(t, func(t *testing.T, s *adapter.Store) {
		ctx := t.Context()
		org := model.NewOrganization(testTenantID, "roles-common", "Roles", "")
		require.NoError(t, s.CreateOrg(ctx, org))
		user := model.NewUser(testTenantID, "", "", "member@example.test", "Member", true)
		require.NoError(t, s.SaveUser(ctx, user))
		require.NoError(t, s.SetCommonMembership(ctx, org.ID(), user.ID(), model.MembershipRoleAdmin, model.StatusActive))
		m, err := s.GetUserOrgMembership(ctx, user.ID(), org.ID())
		require.NoError(t, err)
		custom := model.NewRole(org.ID(), "custom", "")
		require.NoError(t, s.CreateRole(ctx, custom))
		ids := []model.RoleID{custom.ID()}
		for _, r := range m.Roles() {
			ids = append(ids, r.ID())
		}
		require.NoError(t, s.SetMembershipRoles(ctx, m.ID(), ids))
		require.NoError(t, s.SetCommonMembership(ctx, org.ID(), user.ID(), model.MembershipRoleMember, model.StatusActive))
		m, err = s.GetMembership(ctx, m.ID())
		require.NoError(t, err)
		require.Len(t, m.Roles(), 2)
		require.Equal(t, model.MembershipRoleMember, m.CommonRole())
		require.NoError(t, s.SaveOrg(ctx, model.UpdateOrganization(org, model.WithOrgActive(false))))
		permissions, err := s.ResolveEffectivePermissions(ctx, user.ID(), org.ID())
		require.NoError(t, err)
		require.False(t, permissions.IsOwner())
		m, err = s.GetMembership(ctx, m.ID())
		require.NoError(t, err)
		require.Equal(t, model.StatusActive, m.Status())
	})
}
func TestCommonConcurrentOwnersAndPublication(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		s := adapter.NewStore(db)
		require.NoError(t, s.Migrate(ctx))
		tenant := model.NewTenant("concurrent", "Concurrent", "")
		require.NoError(t, s.CreateTenant(ctx, tenant))
		users := []*model.BaseUser{model.NewUser(tenant.ID(), "", "", "a@example.test", "A", true), model.NewUser(tenant.ID(), "", "", "b@example.test", "B", true)}
		for _, u := range users {
			u.SetTenantRole(model.TenantRoleOwner)
			require.NoError(t, s.SaveUser(ctx, u))
		}
		barrier := make(chan struct{})
		results := make(chan error, 2)
		for _, u := range users {
			go func() {
				<-barrier
				copy := model.CopyUser(u)
				copy.SetTenantRole(model.TenantRoleMember)
				results <- s.SaveUser(ctx, copy)
			}()
		}
		close(barrier)
		errorsCount := 0
		for range 2 {
			err := <-results
			if err != nil {
				require.ErrorIs(t, err, port.ErrNotAllowed)
				errorsCount++
			}
		}
		require.Equal(t, 1, errorsCount)
		entered := make(chan struct{})
		release := make(chan struct{})
		second := make(chan struct{})
		done := make(chan error, 2)
		go func() {
			done <- s.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
				close(entered)
				<-release
				return tx.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName("first")))
			})
		}()
		<-entered
		go func() {
			close(second)
			done <- s.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
				return tx.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName("second")))
			})
		}()
		<-second
		select {
		case err := <-done:
			t.Fatalf("writer passed open transaction: %v", err)
		case <-time.After(30 * time.Millisecond):
		}
		close(release)
		require.NoError(t, <-done)
		require.NoError(t, <-done)
		var publications []adapter.Publication
		require.NoError(t, db.Order("sequence").Find(&publications).Error)
		for i, p := range publications {
			require.Equal(t, int64(i+1), p.Sequence)
		}
		require.Contains(t, publications[len(publications)-1].Payload, "second")
	})
}
func TestCommonRecovery(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprintf("automatic=%v", automatic), func(t *testing.T) {
			eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
				ctx := t.Context()
				s := adapter.NewStore(db)
				require.NoError(t, s.Migrate(ctx))
				// Simulate the previous release with actual xid keys, foreign keys enabled,
				// an optional provider identity and a personal secret scope.
				tenantID, orgID, userID := xid.New().String(), xid.New().String(), xid.New().String()
				require.NoError(t, db.Create(&adapter.Tenant{ID: tenantID, Slug: "legacy", Name: "Legacy", Active: 1}).Error)
				require.NoError(t, db.Create(&adapter.Organization{ID: orgID, TenantID: tenantID, Slug: "legacy", Name: "Legacy", Active: 1, Currency: "EUR"}).Error)
				require.NoError(t, db.Create(&adapter.User{ID: userID, TenantID: tenantID, Provider: "oidc", Subject: "subject", Email: "Legacy@Example.test", Active: true}).Error)
				secretKey := "0000000000000000000000000000000000000000000000000000000000000000"
				encrypted, err := crypto.Encrypt(secretKey, "fixture-secret")
				require.NoError(t, err)
				require.NoError(t, db.Create(&adapter.PluginNodeSecret{ID: "secret", OrgID: "~:" + userID, PluginName: "test", NodeID: "node", Key: "secret", ValueEncrypted: encrypted}).Error)
				require.NoError(t, db.Create(&adapter.Membership{ID: "membership", UserID: userID, OrgID: orgID}).Error)
				require.NoError(t, db.Create(&adapter.Role{ID: "legacy-owner", OrgID: orgID, Name: "Owner", Builtin: true, BuiltinKind: "owner"}).Error)
				require.NoError(t, db.Create(&adapter.MembershipRole{MembershipID: "membership", RoleID: "legacy-owner"}).Error)
				require.NoError(t, db.Create(&adapter.AuthToken{ID: "legacy-token", OwnerID: &userID, OrgID: orgID, Value: crypto.HashToken("fixture-key")}).Error)
				require.NoError(t, db.Create(&adapter.Quota{ID: "legacy-quota", Scope: "user", ScopeID: userID, Currency: "EUR"}).Error)
				// Exercise the old schema, not just old values in the new columns.
				require.NoError(t, db.Exec("ALTER TABLE users DROP COLUMN tenant_role").Error)
				require.NoError(t, db.Exec("ALTER TABLE memberships DROP COLUMN common_role").Error)
				require.NoError(t, db.Exec("ALTER TABLE memberships DROP COLUMN status").Error)
				require.NoError(t, db.Migrator().DropTable(&adapter.Domain{}, &adapter.ReservedDomain{}, &adapter.Publication{}, &adapter.MutationAudit{}, &adapter.PublicationClock{}))
				require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610020001").Error)
				artifact, err := adapter.PlanCommonRecovery(ctx, db)
				require.NoError(t, err)
				report, err := adapter.DiagnoseCommonRecovery(ctx, db, artifact)
				require.NoError(t, err)
				require.Empty(t, report.Issues)
				artifact.TenantOwners[tenantID] = []string{userID}
				artifact.Domains = []model.Domain{{Hostname: "legacy.example.test", TenantID: model.TenantID(tenantID), Status: model.StatusActive}}
				report, err = adapter.DiagnoseCommonRecovery(ctx, db, artifact)
				require.NoError(t, err)
				require.Empty(t, report.Issues)
				if automatic {
					require.NoError(t, db.Create(&adapter.UserRole{UserID: userID, Role: "admin"}).Error)
					for _, role := range []adapter.Role{
						{ID: "legacy-admin", OrgID: orgID, Name: "Admin", Builtin: true, BuiltinKind: "admin"},
						{ID: "legacy-custom", OrgID: orgID, Name: "Custom"},
					} {
						require.NoError(t, db.Create(&role).Error)
						require.NoError(t, db.Create(&adapter.MembershipRole{MembershipID: "membership", RoleID: role.ID}).Error)
					}
				}
				if db.Dialector.Name() == "sqlite" {
					// Use one physical connection to verify its FK setting survives
					// rollback before it returns to the pool.
					pool, err := db.DB()
					require.NoError(t, err)
					pool.SetMaxOpenConns(1)
					require.NoError(t, db.Exec("PRAGMA foreign_keys = ON").Error)
				}
				// An interruption before the checkpoint rolls back all rewritten references.
				require.NoError(t, db.Callback().Create().Before("gorm:create").Register("interrupt_recovery", func(tx *gormpkg.DB) {
					if tx.Statement.Table == "common_recoveries" {
						tx.AddError(fmt.Errorf("interrupted"))
					}
				}))
				if automatic {
					err = adapter.NewStore(db).Migrate(ctx)
				} else {
					_, err = adapter.ApplyCommonRecovery(ctx, db, artifact)
				}
				require.Error(t, err)
				require.NoError(t, db.Callback().Create().Remove("interrupt_recovery"))
				if db.Dialector.Name() == "sqlite" {
					var enabled int
					require.NoError(t, db.Raw("PRAGMA foreign_keys").Scan(&enabled).Error)
					require.Equal(t, 1, enabled)
					pool, err := db.DB()
					require.NoError(t, err)
					pool.SetMaxOpenConns(0)
				}
				var original adapter.User
				require.NoError(t, db.Select("id, tenant_id, provider, subject, email, active").First(&original, "id = ?", userID).Error)
				if automatic {
					// Independent startup instances race on the same legacy database.
					results := make(chan error, 3)
					for range 3 {
						go func() { results <- adapter.NewStore(db).Migrate(ctx) }()
					}
					for range 3 {
						require.NoError(t, <-results)
					}
					var checkpoint adapter.CommonRecovery
					require.NoError(t, db.First(&checkpoint, 1).Error)
					artifact = &adapter.RecoveryArtifact{}
					require.NoError(t, json.Unmarshal([]byte(checkpoint.Artifact), artifact))
				} else {
					report, err = adapter.ApplyCommonRecovery(ctx, db, artifact)
					require.NoError(t, err)
					require.True(t, report.Applied)
					_, err = adapter.ApplyCommonRecovery(ctx, db, artifact)
					require.NoError(t, err)
				}
				migrated := adapter.NewStore(db)
				require.NoError(t, migrated.Migrate(ctx))
				user, err := migrated.GetUserByID(ctx, model.UserID(artifact.IDs["users"][userID]))
				require.NoError(t, err)
				if automatic {
					require.Equal(t, model.TenantRoleMember, user.TenantRole())
				} else {
					require.Equal(t, model.TenantRoleOwner, user.TenantRole())
				}
				require.Equal(t, "legacy@example.test", user.Email())
				if automatic {
					var roles []adapter.MembershipRole
					require.NoError(t, db.Where("membership_id = ?", "membership").Find(&roles).Error)
					require.Len(t, roles, 3)
					var platformRole adapter.UserRole
					require.NoError(t, db.First(&platformRole, "user_id = ?", user.ID()).Error)
					require.Equal(t, "admin", platformRole.Role)
				}

				membership, err := migrated.GetMembership(ctx, "membership")
				require.NoError(t, err)
				require.Equal(t, model.MembershipRoleOwner, membership.CommonRole())
				require.Equal(t, model.StatusActive, membership.Status())
				token, err := migrated.FindAuthToken(ctx, "fixture-key")
				require.NoError(t, err)
				require.Equal(t, user.ID(), token.Owner().ID())
				require.Equal(t, model.OrgID(artifact.IDs["organizations"][orgID]), token.OrgID())
				var quota adapter.Quota
				require.NoError(t, db.First(&quota, "id = ?", "legacy-quota").Error)
				require.Equal(t, string(user.ID()), quota.ScopeID)

				var secret adapter.PluginNodeSecret
				require.NoError(t, db.First(&secret, "id = ?", "secret").Error)
				require.Equal(t, "~:"+string(user.ID()), secret.OrgID)
				require.Equal(t, encrypted, secret.ValueEncrypted)
				plaintext, err := crypto.Decrypt(secretKey, secret.ValueEncrypted)
				require.NoError(t, err)
				require.Equal(t, "fixture-secret", plaintext)
				var count int64
				require.NoError(t, db.Model(&adapter.Publication{}).Count(&count).Error)
				require.Zero(t, count)
			})
		})
	}
}

func TestCommonDomainsAndIsolation(t *testing.T) {
	eachBackend(t, func(t *testing.T, s *adapter.Store) {
		ctx := t.Context()
		other := model.NewTenant("domain-other", "Other", "")
		require.NoError(t, s.CreateTenant(ctx, other))
		require.NoError(t, s.ReserveDomain(ctx, "shared.example.test"))
		require.ErrorIs(t, s.SaveDomain(ctx, model.Domain{Hostname: "shared.example.test", TenantID: testTenantID, Status: model.StatusActive}), port.ErrNotAllowed)
		d := model.Domain{Hostname: " A.Example.test ", TenantID: testTenantID, Status: model.StatusActive}
		require.NoError(t, s.SaveDomain(ctx, d))
		got, err := s.GetDomain(ctx, "a.example.test")
		require.NoError(t, err)
		require.Equal(t, testTenantID, got.TenantID)
		d.TenantID = other.ID()
		require.ErrorIs(t, s.SaveDomain(ctx, d), port.ErrAlreadyExists)
		user := model.NewUser(testTenantID, "", "", "external@example.test", "External", true)
		user.SetID("12345678-1234-5678-1234-123456789abc")
		require.NoError(t, s.SaveUser(ctx, user))
		org := model.NewOrganization(other.ID(), "other", "Other", "")
		require.NoError(t, s.CreateOrg(ctx, org))
		require.ErrorIs(t, s.AddMember(ctx, model.NewMembership(user.ID(), org.ID())), port.ErrNotFound)
		require.ErrorIs(t, s.SetCommonMembership(ctx, org.ID(), user.ID(), model.MembershipRoleOwner, model.StatusActive), port.ErrNotFound)
	})
}
func TestCommonLastOrganizationOwner(t *testing.T) {
	eachBackend(t, func(t *testing.T, s *adapter.Store) {
		ctx := t.Context()
		org := model.NewOrganization(testTenantID, "owner-test", "Owners", "")
		require.NoError(t, s.CreateOrg(ctx, org))
		user := model.NewUser(testTenantID, "", "", "owner@example.test", "Owner", true)
		require.NoError(t, s.SaveUser(ctx, user))
		require.NoError(t, s.SetCommonMembership(ctx, org.ID(), user.ID(), model.MembershipRoleOwner, model.StatusActive))
		require.ErrorIs(t, s.SetCommonMembership(ctx, org.ID(), user.ID(), model.MembershipRoleMember, model.StatusActive), port.ErrNotAllowed)
		require.ErrorIs(t, s.SetCommonMembership(ctx, org.ID(), user.ID(), model.MembershipRoleOwner, model.StatusSuspended), port.ErrNotAllowed)
		require.ErrorIs(t, s.DeleteUser(ctx, user.ID()), port.ErrNotAllowed)
		member, err := s.GetUserOrgMembership(ctx, user.ID(), org.ID())
		require.NoError(t, err)
		require.Equal(t, model.MembershipRoleOwner, member.CommonRole())
		// Member suspension does not redefine the declared membership owner count.
		user.SetActive(false)
		require.NoError(t, s.SaveUser(ctx, user))
		active, err := s.IsMember(ctx, user.ID(), org.ID())
		require.NoError(t, err)
		require.False(t, active)
		member, err = s.GetMembership(ctx, member.ID())
		require.NoError(t, err)
		require.Equal(t, model.StatusActive, member.Status())
	})
}

func TestCommonRecoveryDiagnosticIsReadOnly(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := adapter.NewStore(db)
		require.NoError(t, store.Migrate(ctx))
		tenant, err := store.GetTenantBySlug(ctx, model.DefaultTenantSlug)
		require.NoError(t, err)
		users := []adapter.User{{ID: string(model.NewUserID()), TenantID: string(tenant.ID()), Email: "Case@example.test", Active: true}, {ID: string(model.NewUserID()), TenantID: string(tenant.ID()), Email: "case@example.test", Active: true}}
		for _, u := range users {
			require.NoError(t, db.Create(&u).Error)
		}
		a, err := adapter.PlanCommonRecovery(ctx, db)
		require.NoError(t, err)
		a.TenantOwners[string(tenant.ID())] = []string{users[0].ID}
		a.Domains = []model.Domain{{Hostname: "shared.example.test", TenantID: tenant.ID(), Status: model.StatusActive}}
		a.ReservedHostnames = []string{"shared.example.test"}
		report, err := adapter.DiagnoseCommonRecovery(ctx, db, a)
		require.NoError(t, err)
		require.Contains(t, fmt.Sprint(report.Issues), "normalized email collision")
		require.Contains(t, fmt.Sprint(report.Issues), "reserved")
		// Automatic startup reports the same collision and rolls back, rather
		// than choosing a surviving account or marking the migration applied.
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610020001").Error)
		err = adapter.NewStore(db).Migrate(ctx)
		require.ErrorContains(t, err, "normalized email collision")
		var markers int64
		require.NoError(t, db.Table("migrations").Where("id = ?", "202610020001").Count(&markers).Error)
		require.Zero(t, markers)
		var original adapter.User
		require.NoError(t, db.First(&original, "id = ?", users[0].ID).Error)
		require.Equal(t, "Case@example.test", original.Email)
		var count int64
		require.NoError(t, db.Model(&adapter.Publication{}).Count(&count).Error)
		require.Zero(t, count)
	})
}
