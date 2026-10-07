package gorm_test

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// provisioningFeedLock mirrors the advisory lock key of the adapter.
const provisioningFeedLock = 867530903

// holdFeedLock opens a transaction holding the feed lock, as a publisher about
// to commit, with one allocated and written position.
func holdFeedLock(t *testing.T, db *gormpkg.DB, orgID model.OrgID) (*gormpkg.DB, int64) {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { tx.Rollback() })
	require.NoError(t, tx.Exec("SELECT pg_advisory_xact_lock(?)", provisioningFeedLock).Error)
	var sequence int64
	require.NoError(t, tx.Raw("SELECT nextval('provisioning_event_seq')").Scan(&sequence).Error)
	payload, err := json.Marshal(model.CommonEvent{SpecVersion: "1.0", ID: uuid.NewString(), Type: "organization.updated.v1", Sequence: strconv.FormatInt(sequence, 10)})
	require.NoError(t, err)
	require.NoError(t, tx.Create(&xologorm.ProvisioningEvent{Sequence: sequence, CreatedAt: time.Now(), TenantID: string(testTenantID), Family: model.FamilyOrganization, ResourceKey: string(orgID), Payload: string(payload)}).Error)
	return tx, sequence
}

func TestProvisioningFeedFollowsCommitOrder(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		if db.Dialector.Name() != "postgres" {
			t.Skip("SQLite serializes every writer")
		}
		store := newSeededStore(t, db)
		ctx := t.Context()
		org := model.NewOrganization(testTenantID, "ordered", "Ordered", "")
		require.NoError(t, store.CreateOrg(ctx, org))
		start, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)

		a, first := holdFeedLock(t, db, org.ID())
		published := make(chan error, 1)
		go func() {
			published <- store.SaveOrg(context.Background(), model.UpdateOrganization(org, model.WithOrgName("Renamed")))
		}()
		select {
		case err := <-published:
			t.Fatalf("a publication overtook an uncommitted one: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		page, err := store.ReadEvents(ctx, start, 10)
		require.NoError(t, err)
		require.Empty(t, page.Items, "an uncommitted position is never visible")
		require.Equal(t, start, page.NextCursor, "the horizon never passes an in-flight publication")

		require.NoError(t, a.Commit().Error)
		require.NoError(t, <-published)
		events, _ := feedSince(t, store, start)
		require.Len(t, events, 2)
		require.Equal(t, strconv.FormatInt(first, 10), events[0].Sequence)
		second, err := strconv.ParseInt(events[1].Sequence, 10, 64)
		require.NoError(t, err)
		require.Greater(t, second, first)
	})
}

// TestProvisioningHotPathTakesNoFeedLock pins the request path: reading a
// user, saving an unchanged user and recording usage never wait for the feed.
func TestProvisioningHotPathTakesNoFeedLock(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		if db.Dialector.Name() != "postgres" {
			t.Skip("SQLite serializes every writer")
		}
		store := newSeededStore(t, db)
		org := model.NewOrganization(testTenantID, "hot", "Hot", "")
		require.NoError(t, store.CreateOrg(t.Context(), org))
		user := model.NewUser(testTenantID, "oidc", "hot", "hot@example.test", "Hot", true, model.PlatformRoleUser)
		require.NoError(t, store.SaveUser(t.Context(), user))
		before := eventCount(t, db)

		holdFeedLock(t, db, org.ID())
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		for range 3 {
			found, err := store.GetUserByIdentity(ctx, testTenantID, "oidc", "hot")
			require.NoError(t, err)
			require.NoError(t, store.SaveUser(ctx, found))
		}
		require.NoError(t, store.RecordUsage(ctx, model.NewUsageRecord(user.ID(), "", org.ID(), "provider", "model", "model", "", 10, 0, 10, 1, "EUR", model.CostSourceComputed, "")))
		require.Equal(t, before, eventCount(t, db))
	})
}

// TestProvisioningConcurrentProjectionFields changes two rows of one
// membership projection concurrently: the projection keeps both changes.
func TestProvisioningConcurrentProjectionFields(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		org := model.NewOrganization(testTenantID, "fields", "Fields", "")
		require.NoError(t, store.CreateOrg(ctx, org))
		require.NoError(t, store.EnsureBuiltinRoles(ctx, org.ID()))
		roles, err := store.ListOrgRoles(ctx, org.ID())
		require.NoError(t, err)
		builtin := map[string]model.RoleID{}
		for _, role := range roles {
			builtin[role.BuiltinKind()] = role.ID()
		}
		scope := model.CommonScope{Family: model.FamilyOrganizationMembership, TenantID: string(testTenantID), OrganizationID: string(org.ID())}
		for i := range 10 {
			user := model.NewUser(testTenantID, "oidc", "fields-"+strconv.Itoa(i), "", "", true, model.PlatformRoleUser)
			require.NoError(t, store.SaveUser(ctx, user))
			membership := model.NewMembership(user.ID(), org.ID())
			require.NoError(t, store.AddMember(ctx, membership))
			require.NoError(t, store.SetMembershipRoles(ctx, membership.ID(), []model.RoleID{builtin[model.BuiltinKindMember]}))

			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				require.NoError(t, store.SetMembershipRoles(context.Background(), membership.ID(), []model.RoleID{builtin[model.BuiltinKindAdmin]}))
			}()
			go func() {
				defer wg.Done()
				require.NoError(t, store.SetMembershipStatus(context.Background(), membership.ID(), model.StatusSuspended))
			}()
			wg.Wait()
			item, err := store.ReadProjection(ctx, scope, string(user.ID()))
			require.NoError(t, err)
			require.JSONEq(t, `{"role":"admin","status":"suspended"}`, string(item.Representation))
		}
	})
}
