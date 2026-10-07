package gorm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	gormpkg "gorm.io/gorm"
)

func syncService(store *xologorm.Store) *service.ProvisioningService {
	return service.NewProvisioningService(store, store, store, store,
		service.WithProvisioningTransaction(store), service.WithProvisioningReader(store), service.WithMultiTenant(true))
}

func ifMatch(t *testing.T, values ...string) model.MatchCondition {
	t.Helper()
	condition, err := model.ParseMatchCondition(values)
	require.NoError(t, err)
	return condition
}

func putSyncTenant(ctx context.Context, svc *service.ProvisioningService, id model.TenantID, name string, condition model.MatchCondition) (model.CommonItem, error) {
	return svc.PutCommon(ctx, model.CommonScope{Family: model.FamilyTenant}, string(id), condition, func(ctx context.Context, tx *service.ProvisioningService) error {
		_, err := tx.PutTenant(ctx, id, service.CommonResource{Slug: "sync-" + string(id)[:8], Name: name, Status: model.StatusActive})
		return err
	})
}

// feedSince reads every event committed after cursor.
func feedSince(t *testing.T, store *xologorm.Store, cursor string) ([]model.CommonEvent, string) {
	t.Helper()
	var events []model.CommonEvent
	for {
		page, err := store.ReadEvents(t.Context(), cursor, 1000)
		require.NoError(t, err)
		events = append(events, page.Items...)
		cursor = page.NextCursor
		if !page.HasMore {
			return events, cursor
		}
	}
}

func eventTypes(events []model.CommonEvent) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func eventCount(t *testing.T, db *gormpkg.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&xologorm.ProvisioningEvent{}).Count(&n).Error)
	return n
}

func revisionOf(t *testing.T, etag string) int64 {
	t.Helper()
	require.True(t, strings.HasPrefix(etag, `W/"`) && strings.HasSuffix(etag, `"`), etag)
	revision, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(etag, `W/"`), `"`), 10, 64)
	require.NoError(t, err)
	return revision
}

func TestProvisioningConditionalWrites(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		svc := syncService(store)
		id := model.TenantID(uuid.NewString())

		_, err := putSyncTenant(t.Context(), svc, id, "Created", ifMatch(t, "*"))
		require.ErrorIs(t, err, port.ErrPreconditionFailed, "If-Match: * never matches a missing resource")
		created, err := putSyncTenant(t.Context(), svc, id, "Created", model.MatchCondition{})
		require.NoError(t, err)
		require.JSONEq(t, `{"slug":"sync-`+string(id)[:8]+`","name":"Created","status":"active"}`, string(created.Representation))
		read, err := store.ReadProjection(t.Context(), model.CommonScope{Family: model.FamilyTenant}, string(id))
		require.NoError(t, err)
		require.Equal(t, created, read)

		events := eventCount(t, db)
		same, err := putSyncTenant(t.Context(), svc, id, "Created", ifMatch(t, created.ETag))
		require.NoError(t, err)
		require.Equal(t, created.ETag, same.ETag, "a no-op keeps its revision")
		require.Equal(t, events, eventCount(t, db), "a no-op publishes nothing")

		updated, err := putSyncTenant(t.Context(), svc, id, "Updated", ifMatch(t, `"stale"`, created.ETag))
		require.NoError(t, err)
		require.Greater(t, revisionOf(t, updated.ETag), revisionOf(t, created.ETag))
		_, err = putSyncTenant(t.Context(), svc, id, "Updated", ifMatch(t, created.ETag))
		require.ErrorIs(t, err, port.ErrPreconditionFailed, "a stale condition fails even for an identical representation")

		// Two writers holding the same ETag: exactly one wins.
		var wg sync.WaitGroup
		results := make([]error, 2)
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, results[i] = putSyncTenant(context.Background(), svc, id, fmt.Sprintf("Concurrent %d", i), ifMatch(t, updated.ETag))
			}()
		}
		wg.Wait()
		var won, refused int
		for _, err := range results {
			switch {
			case err == nil:
				won++
			case errors.Is(err, port.ErrPreconditionFailed):
				refused++
			default:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		require.Equal(t, 1, won)
		require.Equal(t, 1, refused)
	})
}

func TestProvisioningRevisionsIgnoreTheClock(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		svc := syncService(store)
		id := model.TenantID(uuid.NewString())
		frozen := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		db.Config.NowFunc = func() time.Time { return frozen }
		t.Cleanup(func() { db.Config.NowFunc = time.Now })

		var previous int64
		seen := map[string]bool{}
		for i := range 4 {
			if i == 2 {
				// The clock steps back: revisions keep increasing.
				frozen = frozen.Add(-time.Hour)
			}
			item, err := putSyncTenant(t.Context(), svc, id, fmt.Sprintf("Name %d", i), model.MatchCondition{})
			require.NoError(t, err)
			require.False(t, seen[item.ETag], "an ETag never repeats")
			seen[item.ETag] = true
			revision := revisionOf(t, item.ETag)
			require.Greater(t, revision, previous)
			previous = revision
		}
	})
}

func TestProvisioningFeedPublishesLocalWrites(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		svc := syncService(store)
		ctx := t.Context()
		start, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)

		tenant := model.TenantID(testTenantID)
		org := model.NewOrganization(tenant, "feed", "Feed", "")
		require.NoError(t, store.CreateOrg(ctx, org))
		require.NoError(t, store.EnsureBuiltinRoles(ctx, org.ID()))
		require.NoError(t, store.SaveOrg(ctx, model.UpdateOrganization(org, model.WithOrgName("Renamed"))))
		user := model.NewUser(tenant, "oidc", "feed-"+uuid.NewString(), "feed@example.test", "Feed", true, model.PlatformRoleUser)
		require.NoError(t, store.SaveUser(ctx, user))
		require.NoError(t, store.SaveUser(ctx, user), "an identical save publishes nothing")
		membership := model.NewMembership(user.ID(), org.ID())
		require.NoError(t, store.AddMember(ctx, membership))
		require.NoError(t, store.SetMembershipStatus(ctx, membership.ID(), model.StatusSuspended))
		require.NoError(t, store.RemoveMember(ctx, membership.ID()))
		app := model.NewUser(tenant, model.ApplicationProvider, "app-"+uuid.NewString(), "", "", true, model.PlatformRoleUser)
		require.NoError(t, store.SaveUser(ctx, app), "application shadow users are not members")
		second := model.NewMembership(user.ID(), org.ID())
		require.NoError(t, store.AddMember(ctx, second))
		require.NoError(t, store.DeleteOrg(ctx, org.ID()))

		events, end := feedSince(t, store, start)
		require.Equal(t, []string{
			"organization.created.v1",
			"organization.updated.v1",
			"member.created.v1",
			"organization_membership.created.v1",
			"organization_membership.updated.v1",
			"organization_membership.deleted.v1",
			"organization_membership.created.v1",
			"organization_membership.deleted.v1",
			"organization.deleted.v1",
		}, eventTypes(events))
		var previous int64
		for _, event := range events {
			sequence, err := strconv.ParseInt(event.Sequence, 10, 64)
			require.NoError(t, err)
			require.Greater(t, sequence, previous)
			previous = sequence
			require.Len(t, event.RequestID, 32)
			require.Equal(t, "1.0", event.SpecVersion)
			require.True(t, strings.HasPrefix(event.Source, "urn:uuid:"))
			require.Equal(t, string(tenant), event.Data.Key.TenantID)
			if strings.HasSuffix(event.Type, ".deleted.v1") {
				require.Empty(t, event.Data.ETag)
			} else {
				require.Equal(t, model.CommonETag(sequence), event.Data.ETag, "the event carries the revision it wrote")
			}
		}
		payload, err := json.Marshal(events)
		require.NoError(t, err)
		require.NotContains(t, string(payload), "feed@example.test", "events carry no representation")

		// A member is readable with the revision of its last event.
		member, err := svc.GetCommon(ctx, model.CommonScope{Family: model.FamilyMember, TenantID: string(tenant)}, string(user.ID()))
		require.NoError(t, err)
		require.Equal(t, events[2].Data.ETag, member.ETag)
		appScope := model.CommonScope{Family: model.FamilyMember, TenantID: string(tenant)}
		_, err = svc.GetCommon(ctx, appScope, string(app.ID()))
		require.ErrorIs(t, err, port.ErrNotFound)
		_, err = svc.PutCommon(ctx, appScope, string(app.ID()), model.MatchCondition{}, func(ctx context.Context, tx *service.ProvisioningService) error {
			_, err := tx.PutTenantMember(ctx, tenant, app.ID(), service.CommonMember{Email: "app@example.test", TenantRole: model.TenantRoleMember, Status: model.StatusSuspended})
			return err
		})
		require.ErrorIs(t, err, port.ErrNotFound, "an application shadow user is not writable as a member")

		// Caught up: the end cursor is stable and replays nothing, on any store.
		again, err := xologorm.NewStore(db).ReadEvents(ctx, end, 10)
		require.NoError(t, err)
		require.Empty(t, again.Items)
		require.False(t, again.HasMore)
		require.Equal(t, end, again.NextCursor)

		// Pages are stable and resume where they stopped.
		first, err := store.ReadEvents(ctx, start, 4)
		require.NoError(t, err)
		require.True(t, first.HasMore)
		require.Len(t, first.Items, 4)
		rest, _ := feedSince(t, store, first.NextCursor)
		require.Equal(t, events, append(first.Items, rest...))
	})
}

func TestProvisioningInvitationPublishesMembership(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		f := newInvitationFixture(t, store)
		start, err := store.CaptureEventCursor(t.Context())
		require.NoError(t, err)
		inv := f.invite(t, true, "", nil, nil)
		_, err = f.service.Accept(f.ctx, f.tenant.ID(), inv.ID(), f.user.ID())
		require.NoError(t, err)
		events, _ := feedSince(t, store, start)
		require.Equal(t, []string{"organization_membership.created.v1"}, eventTypes(events))
		require.Equal(t, string(f.org.ID()), events[0].Data.Key.OrganizationID)
		require.Equal(t, string(f.user.ID()), events[0].Data.Key.MemberID)
	})
}

func TestProvisioningRollbackPublishesNothing(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		svc := syncService(store)
		id := model.TenantID(uuid.NewString())
		created, err := putSyncTenant(t.Context(), svc, id, "Before", model.MatchCondition{})
		require.NoError(t, err)
		events := eventCount(t, db)
		var audits int64
		require.NoError(t, db.Model(&xologorm.MutationAudit{}).Count(&audits).Error)

		injected := errors.New("injected event failure")
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:fail-events", func(tx *gormpkg.DB) {
			if tx.Statement.Table == "provisioning_events" {
				_ = tx.AddError(injected)
			}
		}))
		_, err = putSyncTenant(t.Context(), svc, id, "After", model.MatchCondition{})
		require.ErrorIs(t, err, injected)
		require.Error(t, store.SaveTenant(t.Context(), model.UpdateTenant(mustTenant(t, store, id), model.WithTenantName("Local"))))
		require.NoError(t, db.Callback().Create().Remove("test:fail-events"))

		read, err := store.ReadProjection(t.Context(), model.CommonScope{Family: model.FamilyTenant}, string(id))
		require.NoError(t, err)
		require.Equal(t, created, read)
		require.Equal(t, "Before", mustTenant(t, store, id).Name())
		require.Equal(t, events, eventCount(t, db))
		var after int64
		require.NoError(t, db.Model(&xologorm.MutationAudit{}).Count(&after).Error)
		require.Equal(t, audits, after, "the audit rolls back with the publication")
	})
}

func mustTenant(t *testing.T, store *xologorm.Store, id model.TenantID) model.Tenant {
	t.Helper()
	tenant, err := store.GetTenantByID(t.Context(), id)
	require.NoError(t, err)
	return tenant
}

func TestProvisioningListsAndCursors(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		tenant := model.TenantID(testTenantID)
		start, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		const total = 1005
		for i := range total {
			require.NoError(t, store.SaveDomain(ctx, model.Domain{Hostname: fmt.Sprintf("d%04d.example.test", i), TenantID: tenant, Status: model.StatusActive}))
		}
		scope := model.CommonScope{Family: model.FamilyTenantDomain, TenantID: string(tenant)}
		page, err := store.ListProjections(ctx, scope, "", 1000)
		require.NoError(t, err)
		require.Len(t, page.Items, 1000)
		require.Equal(t, "d0000.example.test", page.Items[0].Key.Hostname)
		require.NotNil(t, page.NextCursor)
		cursor := *page.NextCursor

		other := model.NewTenant("other", "Other", "")
		require.NoError(t, store.CreateTenant(ctx, other))
		for name, call := range map[string]func() error{
			"other limit": func() error { _, err := store.ListProjections(ctx, scope, cursor, 999); return err },
			"other family": func() error {
				_, err := store.ListProjections(ctx, model.CommonScope{Family: model.FamilyMember, TenantID: string(tenant)}, cursor, 1000)
				return err
			},
			"other parents": func() error {
				_, err := store.ListProjections(ctx, model.CommonScope{Family: model.FamilyTenantDomain, TenantID: string(other.ID())}, cursor, 1000)
				return err
			},
			"as events":      func() error { _, err := store.ReadEvents(ctx, cursor, 10); return err },
			"events as list": func() error { _, err := store.ListProjections(ctx, scope, start, 1000); return err },
			"tampered": func() error {
				_, err := store.ListProjections(ctx, scope, cursor[:len(cursor)-2]+"AA", 1000)
				return err
			},
			"garbage": func() error { _, err := store.ReadEvents(ctx, "garbage", 10); return err },
		} {
			require.ErrorIs(t, call(), port.ErrInvalidCursor, name)
		}

		// A concurrent rename does not disturb the continuation.
		require.NoError(t, store.SaveDomain(ctx, model.Domain{Hostname: "d0001.example.test", TenantID: tenant, Status: model.StatusSuspended}))
		next, err := store.ListProjections(ctx, scope, cursor, 1000)
		require.NoError(t, err)
		require.Len(t, next.Items, total-1000)
		require.Nil(t, next.NextCursor)

		// Domains of another tenant never appear.
		empty, err := store.ListProjections(ctx, model.CommonScope{Family: model.FamilyTenantDomain, TenantID: string(other.ID())}, "", 10)
		require.NoError(t, err)
		require.Empty(t, empty.Items)
		_, err = store.ListProjections(ctx, model.CommonScope{Family: model.FamilyTenantDomain, TenantID: uuid.NewString()}, "", 10)
		require.ErrorIs(t, err, port.ErrParentNotFound)

		// A list cursor expires 24h after the first page, without renewal.
		db.Config.NowFunc = func() time.Time { return time.Now().Add(25 * time.Hour) }
		t.Cleanup(func() { db.Config.NowFunc = time.Now })
		_, err = store.ListProjections(ctx, scope, cursor, 1000)
		require.ErrorIs(t, err, port.ErrCursorExpired)
	})
}

func TestProvisioningRetention(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		tenant := model.TenantID(testTenantID)
		start, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		seeded := eventCount(t, db)
		const total = 1210
		for i := range total {
			require.NoError(t, store.SaveDomain(ctx, model.Domain{Hostname: fmt.Sprintf("r%04d.example.test", i), TenantID: tenant, Status: model.StatusActive}))
		}
		events, _ := feedSince(t, store, start)
		require.Len(t, events, total)
		head, err := store.ReadEvents(ctx, start, 1000)
		require.NoError(t, err)
		middle, err := store.ReadEvents(ctx, head.NextCursor, 100)
		require.NoError(t, err)

		// The first 1100 events are old, then one recent, then the clock went
		// back: the last events look old again but follow a retained one.
		old := time.Now().Add(-48 * time.Hour)
		cut, err := strconv.ParseInt(events[1099].Sequence, 10, 64)
		require.NoError(t, err)
		require.NoError(t, db.Model(&xologorm.ProvisioningEvent{}).Where("sequence <= ?", cut).Update("created_at", old).Error)
		recent, err := strconv.ParseInt(events[1100].Sequence, 10, 64)
		require.NoError(t, err)
		require.NoError(t, db.Model(&xologorm.ProvisioningEvent{}).Where("sequence > ?", recent).Update("created_at", old).Error)

		purged, err := store.PurgeEvents(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		require.EqualValues(t, seeded+1100, purged, "purge crosses batches and stops at the first retained event")
		require.EqualValues(t, total-1100, eventCount(t, db))

		_, err = store.ReadEvents(ctx, start, 10)
		require.ErrorIs(t, err, port.ErrCursorExpired)
		kept, _ := feedSince(t, store, middle.NextCursor)
		require.Equal(t, events[1100:], kept, "a cursor at the floor still resumes")

		again, err := store.PurgeEvents(ctx, time.Now().Add(-24*time.Hour))
		require.NoError(t, err)
		require.Zero(t, again)
	})
}

func TestProvisioningSyncMigration(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := t.Context()
		tenant := model.TenantID(testTenantID)
		org := model.NewOrganization(tenant, "upgraded", "Upgraded", "")
		require.NoError(t, store.CreateOrg(ctx, org))
		require.NoError(t, store.EnsureBuiltinRoles(ctx, org.ID()))
		user := model.NewUser(tenant, "oidc", "upgraded", "upgraded@example.test", "", true, model.PlatformRoleUser)
		require.NoError(t, store.SaveUser(ctx, user))
		require.NoError(t, store.AddMember(ctx, model.NewMembership(user.ID(), org.ID())))
		require.NoError(t, store.SaveDomain(ctx, model.Domain{Hostname: "upgraded.example.test", TenantID: tenant, Status: model.StatusActive}))
		// A membership crossing tenants must not prevent the upgrade.
		foreign := model.NewTenant("foreign", "Foreign", "")
		require.NoError(t, store.CreateTenant(ctx, foreign))
		stray := model.NewUser(foreign.ID(), "oidc", "stray", "stray@example.test", "", true, model.PlatformRoleUser)
		require.NoError(t, store.SaveUser(ctx, stray))
		strayMembership := model.MembershipID(uuid.NewString())
		require.NoError(t, db.Exec("INSERT INTO memberships (id, user_id, org_id, created_at, status) VALUES (?, ?, ?, ?, 'active')", string(strayMembership), string(stray.ID()), string(org.ID()), time.Now()).Error)

		// Reconstruct the schema of the previous release.
		require.NoError(t, db.Migrator().DropTable(&xologorm.ProvisioningProjection{}, &xologorm.ProvisioningEvent{}, &xologorm.ProvisioningFeed{}))
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "202610080001").Error)
		if db.Dialector.Name() == "postgres" {
			require.NoError(t, db.Exec("DROP SEQUENCE provisioning_event_seq").Error)
		}
		disabled := xologorm.NewStore(db, xologorm.WithAutoMigrate(false))
		require.Error(t, disabled.CheckSchema(ctx))
		require.NoError(t, xologorm.NewStore(db).Migrate(ctx))
		require.NoError(t, disabled.CheckSchema(ctx))
		require.Zero(t, eventCount(t, db), "the existing state is read through an inventory")

		read := func() map[string]model.CommonItem {
			items := map[string]model.CommonItem{}
			for _, target := range []struct {
				scope model.CommonScope
				key   string
			}{
				{model.CommonScope{Family: model.FamilyTenant}, string(tenant)},
				{model.CommonScope{Family: model.FamilyOrganization, TenantID: string(tenant)}, string(org.ID())},
				{model.CommonScope{Family: model.FamilyMember, TenantID: string(tenant)}, string(user.ID())},
				{model.CommonScope{Family: model.FamilyOrganizationMembership, TenantID: string(tenant), OrganizationID: string(org.ID())}, string(user.ID())},
				{model.CommonScope{Family: model.FamilyTenantDomain, TenantID: string(tenant)}, "upgraded.example.test"},
			} {
				item, err := xologorm.NewStore(db).ReadProjection(ctx, target.scope, target.key)
				require.NoError(t, err, target.scope.Family)
				items[target.scope.Family] = item
			}
			return items
		}
		items := read()
		revisions := map[string]bool{}
		for _, item := range items {
			revisions[item.ETag] = true
		}
		require.Len(t, revisions, len(items), "every projection has its own revision")
		require.JSONEq(t, `{"role":"member","status":"active"}`, string(items[model.FamilyOrganizationMembership].Representation))
		_, err := store.ReadProjection(ctx, model.CommonScope{Family: model.FamilyOrganizationMembership, TenantID: string(tenant), OrganizationID: string(org.ID())}, string(stray.ID()))
		require.ErrorIs(t, err, port.ErrNotFound)
		require.Equal(t, items, read(), "revisions are persisted")

		// The upgraded feed continues after the backfilled revisions.
		start, err := disabled.CaptureEventCursor(ctx)
		require.NoError(t, err)
		require.NoError(t, disabled.SaveOrg(ctx, model.UpdateOrganization(org, model.WithOrgName("After upgrade"))))
		events, _ := feedSince(t, disabled, start)
		require.Equal(t, []string{"organization.updated.v1"}, eventTypes(events))
		require.Greater(t, revisionOf(t, events[0].Data.ETag), revisionOf(t, items[model.FamilyOrganization].ETag))

		// Unlike the backfill, a write to the cross-tenant membership fails
		// closed and publishes nothing; removing it still works.
		require.ErrorIs(t, disabled.SetMembershipStatus(ctx, strayMembership, model.StatusSuspended), port.ErrParentNotFound)
		require.NoError(t, disabled.RemoveMember(ctx, strayMembership))
		after, _ := feedSince(t, disabled, start)
		require.Equal(t, events, after)
	})
}
