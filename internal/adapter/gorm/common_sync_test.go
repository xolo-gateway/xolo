package gorm_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"gorm.io/gorm"
)

func TestCommonReadPublicationAndConditions(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s := adapter.NewStore(db)
		ctx := t.Context()
		require.NoError(t, s.Migrate(ctx))
		api := service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))
		cursor, err := s.CaptureCommonCursor(ctx)
		require.NoError(t, err)
		tid := model.NewTenantID()
		scope := model.CommonScope{Family: "tenant"}
		put := func(name string, condition model.MatchCondition) (model.CommonItem, error) {
			return api.WriteCommon(ctx, scope, string(tid), condition, func(tx *service.ProvisioningService) error {
				_, err := tx.PutCommonTenant(ctx, tid, service.CommonResource{Slug: "sync", Name: name, Status: "active"})
				return err
			})
		}
		_, err = put("A", model.MatchCondition{Present: true, Any: true})
		require.ErrorIs(t, err, port.ErrPreconditionFailed)
		a, err := put("A", model.MatchCondition{})
		require.NoError(t, err)
		read, err := s.ReadCommon(ctx, scope, string(tid))
		require.NoError(t, err)
		require.Equal(t, a, read)
		again, err := put("A", model.MatchCondition{})
		require.NoError(t, err)
		require.Equal(t, a, again)
		c, err := model.ParseMatchCondition([]string{a.ETag})
		require.NoError(t, err)
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, name := range []string{"B", "C"} {
			go func(name string) { <-start; _, err := put(name, c); results <- err }(name)
		}
		close(start)
		success, failed := 0, 0
		for range 2 {
			err := <-results
			if err == nil {
				success++
			} else {
				require.ErrorIs(t, err, port.ErrPreconditionFailed)
				failed++
			}
		}
		require.Equal(t, 1, success)
		require.Equal(t, 1, failed)
		page, err := s.ReadCommonEvents(ctx, cursor, 100)
		require.NoError(t, err)
		require.Len(t, page.Items, 2)
		require.False(t, page.HasMore)
		require.Equal(t, "tenant.created.v1", page.Items[0].Type)
		require.Equal(t, "tenant.updated.v1", page.Items[1].Type)
		for _, ev := range page.Items {
			require.Regexp(t, `^[a-f0-9]{32}$`, ev.RequestID)
			require.Equal(t, string(tid), ev.Data.Key.TenantID)
			data, err := json.Marshal(ev)
			require.NoError(t, err)
			require.NotContains(t, string(data), `"name"`)
			require.NotContains(t, string(data), `"actor"`)
		}
		current, err := s.ReadCommon(ctx, scope, string(tid))
		require.NoError(t, err)
		require.Equal(t, current.ETag, page.Items[1].Data.ETag)
		var audits []adapter.MutationAudit
		require.NoError(t, db.Order("sequence").Find(&audits).Error)
		require.Contains(t, audits[len(audits)-1].Actor, "urn:xolo:operator:local")
		// A fresh store keeps source, event IDs, cursor signatures and the horizon.
		restarted := adapter.NewStore(db)
		replay, err := restarted.ReadCommonEvents(ctx, cursor, 100)
		require.NoError(t, err)
		require.Equal(t, page, replay)
		require.NoError(t, s.PurgeCommonEvents(ctx, time.Now().Add(time.Hour)))
		_, err = s.ReadCommonEvents(ctx, cursor, 100)
		require.ErrorIs(t, err, port.ErrCursorExpired)
		empty, err := restarted.ReadCommonEvents(ctx, page.NextCursor, 100)
		require.NoError(t, err)
		require.Empty(t, empty.Items)
		require.NotEmpty(t, empty.NextCursor)
		var count int64
		require.NoError(t, db.Model(&adapter.Publication{}).Count(&count).Error)
		require.Zero(t, count)
	})
}
func TestCommonProjectionIdentityAndLocalFacts(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s := adapter.NewStore(db)
		ctx := t.Context()
		require.NoError(t, s.Migrate(ctx))
		tenant, err := s.GetTenantBySlug(ctx, "default")
		require.NoError(t, err)
		api := service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))
		uid := model.NewUserID()
		oid := model.NewOrgID()
		_, err = api.PutCommonOrganization(ctx, tenant.ID(), oid, service.CommonResource{Slug: "sync", Name: "Sync", Status: "active"})
		require.NoError(t, err)
		_, err = api.PutCommonMember(ctx, tenant.ID(), uid, service.CommonMember{Email: "a@b", TenantRole: "member", Status: "active"})
		require.NoError(t, err)
		_, err = api.PutCommonMembership(ctx, tenant.ID(), oid, uid, service.CommonMembership{Role: "member", Status: "active"})
		require.NoError(t, err)
		scope := model.CommonScope{Family: "member", TenantID: string(tenant.ID())}
		before, err := s.ReadCommon(ctx, scope, string(uid))
		require.NoError(t, err)
		cursor, err := s.CaptureCommonCursor(ctx)
		require.NoError(t, err)
		u, err := s.GetUserByID(ctx, uid)
		require.NoError(t, err)
		changed := model.CopyUser(u)
		changed.SetIdentity("oidc", "identity")
		require.NoError(t, s.SaveUser(ctx, changed))
		after, err := s.ReadCommon(ctx, scope, string(uid))
		require.NoError(t, err)
		require.Equal(t, before, after)
		page, err := s.ReadCommonEvents(ctx, cursor, 100)
		require.NoError(t, err)
		require.Empty(t, page.Items)
		require.NoError(t, s.SetCommonMembership(ctx, oid, uid, "admin", "suspended"))
		page, err = s.ReadCommonEvents(ctx, cursor, 100)
		require.NoError(t, err)
		require.Len(t, page.Items, 2)
		require.Equal(t, "organization_membership.status_changed.v1", page.Items[0].Type)
		require.Equal(t, "organization_membership.role_changed.v1", page.Items[1].Type)
		require.Equal(t, page.Items[0].Data.ETag, page.Items[1].Data.ETag)
		require.NotEqual(t, page.Items[0].ID, page.Items[1].ID)
		foreign := model.NewTenant("foreign-sync", "Foreign", "")
		require.NoError(t, s.CreateTenant(ctx, foreign))
		foreignUser := model.NewUser(foreign.ID(), "", "", "x@y", "", true)
		require.NoError(t, s.SaveUser(ctx, foreignUser))
		cursor, err = s.CaptureCommonCursor(ctx)
		require.NoError(t, err)
		require.Error(t, s.AddMember(ctx, model.NewMembership(foreignUser.ID(), oid)))
		moved := model.NewOrganization(foreign.ID(), "moved", "Moved", "")
		moved.SetID(oid)
		require.Error(t, s.SaveOrg(ctx, moved))
		page, err = s.ReadCommonEvents(ctx, cursor, 100)
		require.NoError(t, err)
		require.Empty(t, page.Items)
	})
}
func TestCommonPaginationLargeCollection(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s := adapter.NewStore(db)
		ctx := t.Context()
		require.NoError(t, s.Migrate(ctx))
		tenant, err := s.GetTenantBySlug(ctx, "default")
		require.NoError(t, err)
		// Deterministic immutable keys include more rows than the maximum page size.
		require.NoError(t, s.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
			for i := 1; i <= 1005; i++ {
				u := model.NewUser(tenant.ID(), "", "", fmt.Sprintf("%d@fixture", i), "Old", i%2 == 0)
				u.SetID(model.UserID(fmt.Sprintf("00000000-0000-0000-0000-%012d", i)))
				if err := tx.SaveUser(ctx, u); err != nil {
					return err
				}
			}
			return nil
		}))
		scope := model.CommonScope{Family: "member", TenantID: string(tenant.ID())}
		page, err := s.ListCommon(ctx, scope, "", 1000)
		require.NoError(t, err)
		require.Len(t, page.Items, 1000)
		require.NotNil(t, page.NextCursor)
		require.Equal(t, "00000000-0000-0000-0000-000000000001", page.Items[0].Key.MemberID)
		_, err = s.ListCommon(ctx, scope, *page.NextCursor, 999)
		require.ErrorIs(t, err, port.ErrInvalidCursor)
		_, err = s.ListCommon(ctx, model.CommonScope{Family: "organization", TenantID: scope.TenantID}, *page.NextCursor, 1000)
		require.ErrorIs(t, err, port.ErrInvalidCursor)
		_, err = s.ReadCommonEvents(ctx, *page.NextCursor, 1000)
		require.ErrorIs(t, err, port.ErrInvalidCursor)
		_, err = s.ListCommon(ctx, scope, *page.NextCursor+"x", 1000)
		require.ErrorIs(t, err, port.ErrInvalidCursor)
		u, err := s.GetUserByID(ctx, model.UserID(page.Items[0].Key.MemberID))
		require.NoError(t, err)
		updated := model.CopyUser(u)
		updated.SetDisplayName("Renamed")
		require.NoError(t, s.SaveUser(ctx, updated))
		rest, err := s.ListCommon(ctx, scope, *page.NextCursor, 1000)
		require.NoError(t, err)
		require.Len(t, rest.Items, 5)
		require.Nil(t, rest.NextCursor)
		// Cursor lifetimes do not refresh on continuation.
		oldNow := db.Config.NowFunc
		db.Config.NowFunc = func() time.Time { return oldNow().Add(25 * time.Hour) }
		_, err = s.ListCommon(ctx, scope, *page.NextCursor, 1000)
		db.Config.NowFunc = oldNow
		require.ErrorIs(t, err, port.ErrCursorExpired)
	})
}
func TestCommonHorizonRollbackAndFailure(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s := adapter.NewStore(db)
		ctx := t.Context()
		require.NoError(t, s.Migrate(ctx))
		tenant, err := s.GetTenantBySlug(ctx, "default")
		require.NoError(t, err)
		for _, rollback := range []bool{false, true} {
			t.Run(fmt.Sprint(rollback), func(t *testing.T) {
				cursor, err := s.CaptureCommonCursor(ctx)
				require.NoError(t, err)
				entered, release := make(chan struct{}), make(chan struct{})
				first := make(chan error, 1)
				second := make(chan error, 1)
				captured := make(chan string, 1)
				go func() {
					first <- s.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
						if err := tx.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName(fmt.Sprint("A", rollback)))); err != nil {
							return err
						}
						close(entered)
						<-release
						if rollback {
							return errors.New("rollback A")
						}
						return nil
					})
				}()
				<-entered
				go func() {
					second <- s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName(fmt.Sprint("B", rollback))))
				}()
				go func() {
					token, err := s.CaptureCommonCursor(ctx)
					if err != nil {
						captured <- "error"
					} else {
						captured <- token
					}
				}()
				select {
				case err := <-second:
					t.Fatalf("B passed A: %v", err)
				case <-time.After(30 * time.Millisecond):
				}
				select {
				case <-captured:
					t.Fatal("capture passed A")
				default:
				}
				close(release)
				err = <-first
				if rollback {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.NoError(t, <-second)
				require.NotEqual(t, "error", <-captured)
				page, err := s.ReadCommonEvents(ctx, cursor, 100)
				require.NoError(t, err)
				expected := 2
				if rollback {
					expected = 1
				}
				require.Len(t, page.Items, expected)
			})
		}
		// Inject failure at publication insertion: resource, audit and projection roll back.
		before, err := s.ReadCommon(ctx, model.CommonScope{Family: "tenant"}, string(tenant.ID()))
		require.NoError(t, err)
		cursor, err := s.CaptureCommonCursor(ctx)
		require.NoError(t, err)
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:publication-failure", func(tx *gorm.DB) {
			if tx.Statement.Table == "publications" {
				tx.AddError(errors.New("publication unavailable"))
			}
		}))
		err = s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName("Rejected")))
		require.NoError(t, db.Callback().Create().Remove("test:publication-failure"))
		require.Error(t, err)
		after, err := s.ReadCommon(ctx, model.CommonScope{Family: "tenant"}, string(tenant.ID()))
		require.NoError(t, err)
		require.Equal(t, before, after)
		page, err := s.ReadCommonEvents(ctx, cursor, 100)
		require.NoError(t, err)
		require.Empty(t, page.Items)
	})
}

func TestCommonProjectionUpgradeAndClock(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s := adapter.NewStore(db)
		ctx := t.Context()
		require.NoError(t, s.Migrate(ctx))
		tenant, err := s.GetTenantBySlug(ctx, "default")
		require.NoError(t, err)
		uid := model.NewUserID()
		user := model.NewUser(tenant.ID(), "", "", "upgrade@test", "Before journal", false)
		user.SetID(uid)
		require.NoError(t, s.SaveUser(ctx, user))
		org := model.NewOrganization(tenant.ID(), "upgrade-org", "Upgrade", "")
		require.NoError(t, s.CreateOrg(ctx, org))
		require.NoError(t, s.SetCommonMembership(ctx, org.ID(), uid, model.MembershipRoleMember, model.StatusActive))
		require.False(t, db.Migrator().HasColumn(&adapter.Membership{}, "updated_at"))
		require.NoError(t, s.SaveDomain(ctx, model.Domain{Hostname: "upgrade.example.test", TenantID: tenant.ID(), Status: "active"}))
		// Model the schema before migration 202610020002, with internal outbox data.
		require.NoError(t, db.Migrator().DropTable(&adapter.CommonRecord{}, &adapter.CommonFeed{}))
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610020002").Error)
		restarted := adapter.NewStore(db)
		require.NoError(t, restarted.Migrate(ctx))
		item, err := restarted.ReadCommon(ctx, model.CommonScope{Family: "member", TenantID: string(tenant.ID())}, string(uid))
		require.NoError(t, err)
		require.Contains(t, string(item.Representation), "Before journal")
		membership, err := restarted.ReadCommon(ctx, model.CommonScope{Family: "organization_membership", TenantID: string(tenant.ID()), OrganizationID: string(org.ID())}, string(uid))
		require.NoError(t, err)
		require.NotEmpty(t, membership.ETag)
		require.Contains(t, string(membership.Representation), `"role":"member"`)
		require.Contains(t, string(item.Representation), "suspended")
		_, err = restarted.ReadCommon(ctx, model.CommonScope{Family: "tenant_domain", TenantID: string(tenant.ID())}, "upgrade.example.test")
		require.NoError(t, err)
		var count int64
		require.NoError(t, db.Model(&adapter.Publication{}).Count(&count).Error)
		require.Zero(t, count)
		// A representation timestamp is exactly microsecond based, independent of
		// the ORM entity timestamp and deliberately permits same-microsecond edits.
		now := time.Date(2026, 10, 2, 12, 0, 0, 123456789, time.UTC)
		oldNow := db.Config.NowFunc
		db.Config.NowFunc = func() time.Time { return now }
		defer func() { db.Config.NowFunc = oldNow }()
		user.SetDisplayName("Controlled")
		require.NoError(t, restarted.SaveUser(ctx, user))
		item, err = restarted.ReadCommon(ctx, model.CommonScope{Family: "member", TenantID: string(tenant.ID())}, string(uid))
		require.NoError(t, err)
		require.Equal(t, model.CommonETag(now), item.ETag)
		user.SetDisplayName("Same microsecond")
		require.NoError(t, restarted.SaveUser(ctx, user))
		same, err := restarted.ReadCommon(ctx, model.CommonScope{Family: "member", TenantID: string(tenant.ID())}, string(uid))
		require.NoError(t, err)
		require.Equal(t, item.ETag, same.ETag)
		require.NotEqual(t, item.Representation, same.Representation)
	})
}
func TestCommonRetentionPrefix(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s := adapter.NewStore(db)
		ctx := t.Context()
		require.NoError(t, s.Migrate(ctx))
		tenant, err := s.GetTenantBySlug(ctx, "default")
		require.NoError(t, err)
		first, err := s.CaptureCommonCursor(ctx)
		require.NoError(t, err)
		oldNow := db.Config.NowFunc
		now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
		db.Config.NowFunc = func() time.Time { return now }
		defer func() { db.Config.NowFunc = oldNow }()
		require.NoError(t, s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName("Old"))))
		middle, err := s.CaptureCommonCursor(ctx)
		require.NoError(t, err)
		now = now.Add(time.Hour)
		cutoff := now
		require.NoError(t, s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName("Retained"))))
		now = now.Add(-2 * time.Hour) // Wall-clock reversal must not break the retained prefix.
		require.NoError(t, s.SaveTenant(ctx, model.UpdateTenant(tenant, model.WithTenantName("Also retained"))))
		require.NoError(t, s.PurgeCommonEvents(ctx, cutoff))
		_, err = s.ReadCommonEvents(ctx, first, 100)
		require.ErrorIs(t, err, port.ErrCursorExpired)
		page, err := s.ReadCommonEvents(ctx, middle, 100)
		require.NoError(t, err)
		require.Len(t, page.Items, 2)
	})
}

func TestCommonConcurrentReadsAreCoherent(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s := adapter.NewStore(db)
		ctx := t.Context()
		require.NoError(t, s.Migrate(ctx))
		api := service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))
		tid := model.NewTenantID()
		// Writers provide paired fields; readers may observe either complete version.
		_, err := api.PutCommonTenant(ctx, tid, service.CommonResource{Slug: "a", Name: "A", Status: "active"})
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() {
			for i := range 30 {
				slug, name := "a", "A"
				if i%2 == 0 {
					slug, name = "b", "B"
				}
				_, err := api.PutCommonTenant(ctx, tid, service.CommonResource{Slug: slug, Name: name, Status: "active"})
				if err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
		for range 60 {
			item, err := s.ReadCommon(ctx, model.CommonScope{Family: "tenant"}, string(tid))
			require.NoError(t, err)
			var rep map[string]string
			require.NoError(t, json.Unmarshal(item.Representation, &rep))
			require.Equal(t, strings.ToUpper(rep["slug"]), rep["name"])
			require.Regexp(t, `^W/"u-[0-9]+"$`, item.ETag)
		}
		require.NoError(t, <-done)
	})
}
