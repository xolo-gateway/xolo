package gorm_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"gorm.io/gorm"
)

func TestLifecycleConcurrentWriterAndFreeze(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s := adapter.NewStore(db)
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		require.NoError(t, s.Migrate(ctx))
		tenant, err := s.GetTenantBySlug(ctx, model.DefaultTenantSlug)
		require.NoError(t, err)
		org := model.NewOrganization(tenant.ID(), "concurrent", "Concurrent", "")
		require.NoError(t, s.CreateOrg(ctx, org))
		scope := model.CommonScope{Family: "organization", TenantID: string(tenant.ID())}
		key := string(org.ID())
		locked, release := make(chan struct{}), make(chan struct{})
		writer := make(chan error, 1)
		go func() {
			writer <- s.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
				close(locked)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				return tx.(port.ApplicationStore).CreateApplication(ctx, model.NewApplication(org.ID(), "Before freeze", "", true))
			})
		}()
		<-locked
		deleted := make(chan error, 1)
		started := make(chan struct{})
		go func() {
			close(started)
			_, err := s.ScheduleDeletion(ctx, scope, key, model.MatchCondition{}, time.Second)
			deleted <- err
		}()
		<-started
		close(release)
		require.NoError(t, <-writer)
		require.NoError(t, <-deleted)
		raw, err := s.ExportDeletion(ctx, scope, key)
		require.NoError(t, err)
		var archive struct {
			Payload struct {
				Tables map[string][]any `json:"tables"`
			} `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(raw, &archive))
		require.Len(t, archive.Payload.Tables["applications"], 1)
		// A writer that captured a parent before deletion cannot write using that
		// stale parent after freeze, including through a separate Store instance.
		other := adapter.NewStore(db)
		require.NoError(t, other.Migrate(ctx))
		require.ErrorIs(t, other.CreateApplication(ctx, model.NewApplication(org.ID(), "After freeze", "", true)), port.ErrResourceDeleted)
	})
}
func TestLifecycleMemberPreservesSharedIdentityAndBlocksOldLogin(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s := adapter.NewStore(db)
		ctx := t.Context()
		require.NoError(t, s.Migrate(ctx))
		api := service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))
		identity := model.Identity{Issuer: "https://identity.example", Subject: "shared"}
		proof := service.AuthenticatedIdentity{Provider: "idp", Issuer: identity.Issuer, Subject: identity.Subject, Email: "shared@example.test", EmailVerified: true}
		tids := []model.TenantID{model.NewTenantID(), model.NewTenantID()}
		uids := []model.UserID{model.NewUserID(), model.NewUserID()}
		for i, tid := range tids {
			slug := []string{"one", "two"}[i]
			_, err := api.PutCommonTenant(ctx, tid, service.CommonResource{Slug: slug, Name: slug, Status: "active"})
			require.NoError(t, err)
			_, err = api.PutCommonMember(ctx, tid, uids[i], service.CommonMember{Email: proof.Email, TenantRole: "member", Status: "active", Identity: &identity})
			require.NoError(t, err)
			_, err = service.ResolveAuthenticatedIdentity(ctx, s, tid, proof, service.LoginPolicy{AutoCreate: true})
			require.NoError(t, err)
		}
		session, err := s.OpenSession(ctx, identity.Issuer, identity.Subject, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
		require.NoError(t, err)
		scope := model.CommonScope{Family: "member", TenantID: string(tids[0])}
		key := string(uids[0])
		d, err := s.ScheduleDeletion(ctx, scope, key, model.MatchCondition{}, time.Second)
		require.NoError(t, err)
		_, err = service.ResolveAuthenticatedIdentity(ctx, s, tids[0], proof, service.LoginPolicy{AutoCreate: true})
		require.ErrorIs(t, err, port.ErrResourceDeleted)
		raw, err := s.ExportDeletion(ctx, scope, key)
		require.NoError(t, err)
		var archive struct {
			SHA256 string `json:"sha256"`
		}
		require.NoError(t, json.Unmarshal(raw, &archive))
		c, err := model.ParseMatchCondition([]string{d.ETag})
		require.NoError(t, err)
		_, err = s.ConfirmDeletion(ctx, scope, key, c, archive.SHA256)
		require.NoError(t, err)
		require.NoError(t, db.Model(&adapter.ResourceDeletion{}).Where("resource_id = ?", key).Update("purge_after", time.Now().Add(-time.Hour)).Error)
		require.NoError(t, s.PurgeDeletion(ctx, scope, key))
		require.NoError(t, s.CheckSession(ctx, session, identity.Issuer, identity.Subject))
		other, err := service.ResolveAuthenticatedIdentity(ctx, s, tids[1], proof, service.LoginPolicy{AutoCreate: true})
		require.NoError(t, err)
		require.Equal(t, uids[1], other.ID())
		_, err = service.ResolveAuthenticatedIdentity(ctx, s, tids[0], proof, service.LoginPolicy{AutoCreate: true, ActiveByDefault: true})
		require.ErrorIs(t, err, port.ErrResourceDeleted)
	})
}

func TestLifecycleInflightLoginAndInvitation(t *testing.T) {
	for _, operation := range []string{"login", "invitation"} {
		t.Run(operation, func(t *testing.T) {
			eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				s := adapter.NewStore(db)
				require.NoError(t, s.Migrate(ctx))
				tenant, err := s.GetTenantBySlug(ctx, "default")
				require.NoError(t, err)
				org := model.NewOrganization(tenant.ID(), "inflight", "In flight", "")
				require.NoError(t, s.CreateOrg(ctx, org))
				user := model.NewUser(tenant.ID(), "", "", "waiting@test", "Waiting", true)
				require.NoError(t, s.SaveUser(ctx, user))
				invite := model.NewInviteToken(org.ID(), "member", nil, nil, nil, user.ID())
				require.NoError(t, s.CreateInvite(ctx, invite))
				scope := model.CommonScope{Family: "organization", TenantID: string(tenant.ID())}
				key := string(org.ID())
				var call func(*adapter.Store) error
				if operation == "login" {
					scope.Family = "member"
					key = string(user.ID())
					proof := service.AuthenticatedIdentity{Provider: "idp", Issuer: "https://identity.example", Subject: "inflight", Email: user.Email(), EmailVerified: true}
					call = func(store *adapter.Store) error {
						_, err := service.ResolveAuthenticatedIdentity(ctx, store, tenant.ID(), proof, service.LoginPolicy{AutoCreate: true})
						return err
					}
				} else {
					call = func(store *adapter.Store) error {
						_, err := service.NewInvitationService(store).Accept(ctx, tenant.ID(), invite.ID(), user.ID())
						return err
					}
				}
				locked, release := make(chan struct{}), make(chan struct{})
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				wrote := make(chan error, 1)
				go func() {
					wrote <- s.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
						close(locked)
						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err()
						}
						return call(tx.(*adapter.Store))
					})
				}()
				select {
				case <-locked:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				froze := make(chan error, 1)
				go func() {
					_, err := s.ScheduleDeletion(ctx, scope, key, model.MatchCondition{}, time.Second)
					froze <- err
				}()
				close(release)
				require.NoError(t, <-wrote)
				require.NoError(t, <-froze)
				raw, err := s.ExportDeletion(ctx, scope, key)
				require.NoError(t, err)
				var archive struct {
					Payload struct{ Tables map[string][]map[string]any }
				}
				require.NoError(t, json.Unmarshal(raw, &archive))
				if operation == "login" {
					require.Len(t, archive.Payload.Tables["users"], 1)
					require.Equal(t, "inflight", archive.Payload.Tables["users"][0]["subject"])
				} else {
					require.Len(t, archive.Payload.Tables["memberships"], 1)
				}
				require.Error(t, call(s), "a delayed second operation must not restore access")
			})
		})
	}
}
