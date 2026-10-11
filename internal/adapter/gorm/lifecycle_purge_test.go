package gorm_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/deletion"
	gormpkg "gorm.io/gorm"
)

// exportDeletion exports the scope of a deletion and verifies it.
func exportDeletion(t *testing.T, store *xologorm.Store, scope model.CommonScope, key string) ([]byte, deletion.Summary) {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, service.NewLifecycleService(store).Export(t.Context(), scope, key, &out))
	summary, err := deletion.Verify(bytes.NewReader(out.Bytes()))
	require.NoError(t, err)
	return out.Bytes(), summary
}

// confirmDeletion freezes the resource, confirms the export of its scope and
// lets its retention elapse.
func confirmDeletion(t *testing.T, db *gormpkg.DB, store *xologorm.Store, scope model.CommonScope, key string) model.Deletion {
	t.Helper()
	ctx := t.Context()
	frozen, err := store.FreezeResource(ctx, scope, key, model.MatchCondition{})
	require.NoError(t, err)
	_, summary := exportDeletion(t, store, scope, key)
	confirmed, err := service.NewLifecycleService(store).Confirm(ctx, scope, key, ifMatch(t, frozen.ETag), summary.SHA256)
	require.NoError(t, err)
	require.NotNil(t, confirmed.ConfirmedAt)
	elapse(t, db)
	return confirmed
}

// elapse ends the retention of every deletion.
func elapse(t *testing.T, db *gormpkg.DB) {
	t.Helper()
	require.NoError(t, db.Exec("UPDATE resource_deletions SET purge_after = ?", time.Now().UTC().Add(-time.Hour)).Error)
}

func countRows(t *testing.T, db *gormpkg.DB, table, where string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Table(table).Where(where, args...).Count(&n).Error)
	return n
}

// purgeDue purges every due deletion, as the worker does.
func purgeDue(t *testing.T, store *xologorm.Store) int {
	t.Helper()
	purged, err := service.NewLifecycleService(store).PurgeDue(t.Context())
	require.NoError(t, err)
	return purged
}

// TestDeletionExport streams the scope of a frozen organization: the same
// bytes on every export, its secrets left out, and a trailer whose absence
// invalidates it.
func TestDeletionExport(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		svc := businessService(base)
		newBusinessFixture(t, base, svc, fixture)
		store := lifecycleStore(t, db)

		require.ErrorIs(t, service.NewLifecycleService(store).Export(ctx, orgScope(testTenantID), string(fixture.org), &bytes.Buffer{}), port.ErrNotFound, "nothing to export before the deletion")

		_, err := store.FreezeResource(ctx, orgScope(testTenantID), string(fixture.org), model.MatchCondition{})
		require.NoError(t, err)
		first, summary := exportDeletion(t, store, orgScope(testTenantID), string(fixture.org))
		second, _ := exportDeletion(t, store, orgScope(testTenantID), string(fixture.org))
		require.Equal(t, first, second, "an export does not depend on the time it is made")

		require.Equal(t, string(fixture.org), summary.Deletion.ResourceID)
		require.NotEmpty(t, summary.Deletion.ETag)
		for _, table := range []string{"organizations", "memberships", "roles", "providers", "llm_models", "applications", "quota", "alerts", "mutation_audits"} {
			require.NotZero(t, summary.Tables[table], table)
		}
		require.Zero(t, summary.Tables["users"], "the members of an organization are not its own")
		require.NotContains(t, string(first), "sk-first-secret")
		require.NotContains(t, string(first), `"api_key"`)

		lines := bytes.SplitAfter(first, []byte("\n"))
		truncated := bytes.Join(lines[:len(lines)-2], nil)
		_, err = deletion.Verify(bytes.NewReader(truncated))
		require.ErrorIs(t, err, deletion.ErrInvalidExport, "an export without trailer is invalid")
	})
}

// TestConfirmDeletion confirms an export only under the explicit revision of
// the deleted resource, and with the digest of its current export.
func TestConfirmDeletion(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		store := lifecycleStore(t, db)
		lifecycle := service.NewLifecycleService(store)
		scope, key := orgScope(testTenantID), string(fixture.org)

		frozen, err := store.FreezeResource(ctx, scope, key, model.MatchCondition{})
		require.NoError(t, err)
		_, summary := exportDeletion(t, store, scope, key)

		_, err = lifecycle.Confirm(ctx, scope, key, model.MatchCondition{}, summary.SHA256)
		require.ErrorIs(t, err, port.ErrConfirmationRequired)
		_, err = lifecycle.Confirm(ctx, scope, key, ifMatch(t, "*"), summary.SHA256)
		require.ErrorIs(t, err, port.ErrConfirmationRequired)
		_, err = lifecycle.Confirm(ctx, scope, key, ifMatch(t, frozen.ETag), strings.ToUpper(summary.SHA256))
		require.ErrorIs(t, err, port.ErrInvalid)
		_, err = lifecycle.Confirm(ctx, scope, key, ifMatch(t, frozen.ETag), strings.Repeat("0", 64))
		require.ErrorIs(t, err, port.ErrExportMismatch)
		_, err = lifecycle.Confirm(ctx, scope, key, ifMatch(t, `W/"1"`), summary.SHA256)
		require.ErrorIs(t, err, port.ErrPreconditionFailed)

		require.ErrorIs(t, store.PurgeDeletion(ctx, frozen), port.ErrPurgeNotReady, "not confirmed")

		confirmed, err := lifecycle.Confirm(ctx, scope, key, ifMatch(t, frozen.ETag), summary.SHA256)
		require.NoError(t, err)
		require.Equal(t, summary.SHA256, confirmed.ExportSHA256)
		again, err := lifecycle.Confirm(ctx, scope, key, ifMatch(t, frozen.ETag), summary.SHA256)
		require.NoError(t, err)
		require.Equal(t, confirmed, again, "a repeated confirmation")
		_, err = store.ConfirmDeletion(ctx, scope, key, ifMatch(t, frozen.ETag), strings.Repeat("a", 64))
		require.ErrorIs(t, err, port.ErrExportMismatch, "another digest once confirmed")

		due, err := store.DueDeletions(ctx, 10)
		require.NoError(t, err)
		require.Empty(t, due, "the retention has not elapsed")
		require.ErrorIs(t, store.PurgeDeletion(ctx, confirmed), port.ErrPurgeNotReady)
		require.True(t, exists(t, db, "organizations", string(fixture.org)))

		// A purge in between shrinks the scope of another deletion: its
		// client exports it again.
		inner := newOwnershipFixture(t, base)
		innerFrozen, err := store.FreezeResource(ctx, scope, string(inner.org), model.MatchCondition{})
		require.NoError(t, err)
		_, stale := exportDeletion(t, store, scope, string(inner.org))
		confirmDeletion(t, db, store, memberScope(testTenantID), string(inner.user))
		require.Equal(t, 2, purgeDue(t, store), "the first organization, and the member whose membership the second holds too")
		_, err = lifecycle.Confirm(ctx, scope, string(inner.org), ifMatch(t, innerFrozen.ETag), stale.SHA256)
		require.ErrorIs(t, err, port.ErrExportMismatch)
		_, fresh := exportDeletion(t, store, scope, string(inner.org))
		require.Less(t, fresh.Count, stale.Count)
		_, err = lifecycle.Confirm(ctx, scope, string(inner.org), ifMatch(t, innerFrozen.ETag), fresh.SHA256)
		require.NoError(t, err)
	})
}

func exists(t *testing.T, db *gormpkg.DB, table, id string) bool {
	t.Helper()
	return countRows(t, db, table, "id = ?", id) == 1
}

// TestPurgeOrganization purges a confirmed organization once its retention
// has elapsed: every row of its scope goes, its deletion is published, and
// its identifier stays retired.
func TestPurgeOrganization(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		svc := businessService(base)
		business := newBusinessFixture(t, base, svc, fixture)
		bystander := newOwnershipFixture(t, base)
		org := string(fixture.org)
		require.NoError(t, base.RecordUsage(ctx, model.NewUsageRecord(fixture.user, "", fixture.org, "", model.ProviderID(business.provider), business.llm,
			"fast", "", 100, 10, 50, 1_000, "EUR", model.CostSourceComputed, "")))
		require.NoError(t, base.RecordEvent(ctx, model.NewEvent("test", "llm.request.failed", model.WithEventOrg(fixture.org), model.WithEventUser(fixture.user))))
		hook := putHook(t, base, testTenantID)
		touchTenant(t, base, testTenantID)
		store := lifecycleStore(t, db)

		confirmed := confirmDeletion(t, db, store, orgScope(testTenantID), org)
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 1000))
		require.NotZero(t, countRows(t, db, "webhook_deliveries", "subscription_id = ? AND body LIKE ?", string(hook.ID), "%"+org+"%"))
		kept := countRows(t, db, "webhook_deliveries", "subscription_id = ? AND body NOT LIKE ?", string(hook.ID), "%"+org+"%")
		require.NotZero(t, kept)
		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		var floor xologorm.ProvisioningFeed
		require.NoError(t, db.First(&floor).Error)

		require.Equal(t, 1, purgeDue(t, store))
		for table, column := range map[string]string{
			"organizations": "id", "memberships": "org_id", "roles": "org_id", "providers": "org_id", "llm_models": "org_id",
			"applications": "org_id", "alerts": "org_id", "usage_records": "org_id", "events": "org_id",
		} {
			require.Zero(t, countRows(t, db, table, column+" = ?", org), table)
		}
		require.Zero(t, countRows(t, db, "quota", "id = ?", business.quota))
		require.Zero(t, countRows(t, db, "provisioning_projections", "org_id = ? OR resource_key IN ?", org, []string{org, business.quota}))
		require.Zero(t, countRows(t, db, "provisioning_events", "(org_id = ? OR resource_key IN ?) AND payload NOT LIKE ?", org, []string{org, business.quota}, "%.deleted.v1%"),
			"the events about the scope go, except its deletion")
		require.Zero(t, countRows(t, db, "mutation_audits", "org_id = ? AND resource <> 'deletion'", org))
		require.True(t, exists(t, db, "users", string(fixture.user)), "the members belong to the tenant")
		require.True(t, exists(t, db, "organizations", string(bystander.org)))

		require.Zero(t, countRows(t, db, "webhook_deliveries", "subscription_id = ? AND body LIKE ? AND body NOT LIKE ?", string(hook.ID), "%"+org+"%", "%.deleted.v1%"),
			"the deliveries of the events removed go")
		require.Equal(t, kept, countRows(t, db, "webhook_deliveries", "subscription_id = ? AND body NOT LIKE ?", string(hook.ID), "%"+org+"%"),
			"the other deliveries of the tenant stay")

		events, _ := feedSince(t, store, cursor)
		require.ElementsMatch(t, []string{"organization.deleted.v1", "quota.deleted.v1"}, eventTypes(events))
		var after xologorm.ProvisioningFeed
		require.NoError(t, db.First(&after).Error)
		require.Equal(t, floor.Floor, after.Floor, "the feed floor never moves")

		purged, err := store.ReadDeletion(ctx, orgScope(testTenantID), org)
		require.NoError(t, err)
		require.NotNil(t, purged.PurgedAt)
		require.Equal(t, model.DeletionPurged, purged.Diagnostic)
		require.Empty(t, purged.ETag)
		require.NoError(t, store.PurgeDeletion(ctx, confirmed), "a purged deletion is left as is")
		require.ErrorIs(t, service.NewLifecycleService(store).Export(ctx, orgScope(testTenantID), org, &bytes.Buffer{}), port.ErrNotFound)

		_, err = svc.PutOrganization(ctx, testTenantID, fixture.org, service.CommonResource{Slug: "again", Name: "Again", Status: model.StatusActive})
		require.ErrorIs(t, err, port.ErrResourceDeleted, "a retired identifier is never reused")
	})
}

// TestPurgeTenantKeepsOtherTenants purges a tenant while another one holds a
// webhook backlog: the other tenant keeps its subscription, its backlog and
// its history.
func TestPurgeTenantKeepsOtherTenants(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		store := lifecycleStore(t, db)
		purgedTenant := newWebhookTenant(t, base, "purged")
		purgedHook := putHook(t, base, purgedTenant)
		org := model.NewOrganization(purgedTenant, "inside", "Inside", "")
		require.NoError(t, base.CreateOrg(ctx, org))
		member := model.NewUser(purgedTenant, "oidc", uuid.NewString(), "inside@example.test", "Inside", true, model.PlatformRoleUser)
		require.NoError(t, base.SaveUser(ctx, member))
		touchTenant(t, base, purgedTenant)

		kept := newWebhookTenant(t, base, "kept")
		keptHook := putHook(t, base, kept)
		touchTenant(t, base, kept)
		touchTenant(t, base, kept)
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		backlog := hookDeliveries(t, db, keptHook)
		require.NotEmpty(t, backlog)
		require.NotEmpty(t, hookDeliveries(t, db, purgedHook))
		var feed xologorm.ProvisioningFeed
		require.NoError(t, db.First(&feed).Error)

		// An organization deletion recorded before the tenant's: the tenant
		// takes it along.
		_, err := store.FreezeResource(ctx, orgScope(purgedTenant), string(org.ID()), model.MatchCondition{})
		require.NoError(t, err)
		confirmDeletion(t, db, store, tenantScope, string(purgedTenant))
		due, err := store.DueDeletions(ctx, 10)
		require.NoError(t, err)
		require.Len(t, due, 1)
		require.Equal(t, model.FamilyTenant, due[0].Family)
		require.Equal(t, 1, purgeDue(t, store))

		require.False(t, exists(t, db, "tenants", string(purgedTenant)))
		require.Zero(t, countRows(t, db, "users", "tenant_id = ?", string(purgedTenant)))
		require.Zero(t, countRows(t, db, "webhook_subscriptions", "tenant_id = ?", string(purgedTenant)))
		require.Zero(t, countRows(t, db, "webhook_deliveries", "tenant_id = ?", string(purgedTenant)))
		require.Zero(t, countRows(t, db, "provisioning_projections", "tenant_id = ?", string(purgedTenant)))
		require.Equal(t, int64(1), countRows(t, db, "provisioning_events", "tenant_id = ?", string(purgedTenant)), "its deletion only")
		for _, child := range []struct {
			scope model.CommonScope
			key   string
		}{{orgScope(purgedTenant), string(org.ID())}, {memberScope(purgedTenant), string(member.ID())}} {
			retired, err := store.ReadDeletion(ctx, child.scope, child.key)
			require.NoError(t, err)
			require.NotNil(t, retired.PurgedAt)
			require.Equal(t, string(purgedTenant), retired.PurgedWith)
		}

		var row xologorm.WebhookSubscription
		require.NoError(t, db.Take(&row, "id = ?", string(keptHook.ID)).Error)
		require.Equal(t, model.WebhookReady, row.State)
		require.Equal(t, backlog, hookDeliveries(t, db, keptHook), "the backlog of the other tenant is intact")
		var after xologorm.ProvisioningFeed
		require.NoError(t, db.First(&after).Error)
		require.Equal(t, feed.Floor, after.Floor)
		require.NoError(t, store.PrepareWebhooks(ctx, roomyWebhooks, 100))
		require.NoError(t, db.Take(&row, "id = ?", string(keptHook.ID)).Error)
		require.Equal(t, model.WebhookReady, row.State, "no history lost")

		_, err = base.GetTenantBySlug(ctx, "purged")
		require.ErrorIs(t, err, port.ErrNotFound)
		reused := model.NewTenant("purged", "Purged", "")
		reused.SetID(purgedTenant)
		require.ErrorIs(t, base.CreateTenant(ctx, reused), port.ErrResourceDeleted, "a retired identifier is never reused")
		require.NoError(t, base.CreateTenant(ctx, model.NewTenant("purged", "Purged", "")), "the slug is free again")
	})
}

// TestPurgeMember purges a member: its account and what it holds go, the
// audits of its operations are anonymized, and the shared alerts it owned
// are detached from it.
func TestPurgeMember(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		svc := businessService(base)
		user := string(fixture.user)
		personal, shared := uuid.NewString(), uuid.NewString()
		_, err := svc.PutAlert(ctx, testTenantID, fixture.org, personal, noCondition, alertSettings(fixture.user, 3))
		require.NoError(t, err)
		sharedSettings := alertSettings(fixture.user, 5)
		sharedSettings.Scope = model.AlertScopeOrg
		_, err = svc.PutAlert(ctx, testTenantID, fixture.org, shared, noCondition, sharedSettings)
		require.NoError(t, err)
		require.NoError(t, base.RecordEvent(ctx, model.NewEvent("test", "llm.request.failed", model.WithEventOrg(fixture.org), model.WithEventUser(fixture.user))))
		// An operation of the member, audited.
		acting := model.WithActor(ctx, model.Actor{UserID: fixture.user, RequestID: "req-member"})
		_, err = svc.PutOrganization(acting, testTenantID, model.NewOrgID(), service.CommonResource{Slug: "made-by-member", Name: "Made", Status: model.StatusActive})
		require.NoError(t, err)
		elsewhere := newWebhookTenant(t, base, "elsewhere")
		touchTenant(t, base, elsewhere)
		others := countRows(t, db, "mutation_audits", "tenant_id <> ?", string(testTenantID))
		store := lifecycleStore(t, db)

		confirmDeletion(t, db, store, memberScope(testTenantID), user)
		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, purgeDue(t, store))

		require.False(t, exists(t, db, "users", user))
		require.Zero(t, countRows(t, db, "memberships", "user_id = ?", user))
		require.Zero(t, countRows(t, db, "events", "user_id = ?", user))
		require.False(t, exists(t, db, "alerts", personal))
		require.Equal(t, int64(1), countRows(t, db, "alerts", "id = ? AND owner_id = ''", shared), "the shared alert outlives its owner")
		require.Zero(t, countRows(t, db, "mutation_audits", "resource_id = ? AND resource <> 'deletion'", user))
		require.Zero(t, countRows(t, db, "mutation_audits", "request_id = ? AND actor LIKE ?", "req-member", "%"+user+"%"))
		require.NotZero(t, countRows(t, db, "mutation_audits", "request_id = ?", "req-member"), "the operations stay audited")
		require.Equal(t, others, countRows(t, db, "mutation_audits", "tenant_id <> ?", string(testTenantID)))
		require.True(t, exists(t, db, "organizations", string(fixture.org)))

		events, _ := feedSince(t, store, cursor)
		require.ElementsMatch(t, []string{"member.deleted.v1", "organization_membership.deleted.v1", "alert.deleted.v1", "alert.updated.v1"}, eventTypes(events))
	})
}

// TestPurgeMemberDetachesTeams purges a member of a team holding a budget in
// it: the team outlives the member, which leaves it with its team roles and
// budget, and the team and quota projections are published again.
func TestPurgeMemberDetachesTeams(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		svc := businessService(base)
		f := newBusinessFixture(t, base, svc, fixture)
		budget := uuid.NewString()
		_, err := svc.PutQuota(ctx, testTenantID, budget, noCondition, model.QuotaSettings{
			Scope: model.QuotaScopeTeamMember, ScopeID: model.TeamMemberScopeID(model.TeamID(f.team), f.user), Currency: "EUR", MonthlyBudget: ptrTo(int64(100)),
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), countRows(t, db, "team_members", "team_id = ?", f.team))
		require.Equal(t, int64(1), countRows(t, db, "team_member_roles", "team_id = ?", f.team))
		store := lifecycleStore(t, db)

		confirmDeletion(t, db, store, memberScope(testTenantID), string(f.user))
		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, purgeDue(t, store))

		require.True(t, exists(t, db, "teams", f.team), "the team outlives its member")
		require.Zero(t, countRows(t, db, "team_members", "team_id = ?", f.team))
		require.Zero(t, countRows(t, db, "team_member_roles", "team_id = ?", f.team))
		require.False(t, exists(t, db, "quota", budget), "the member budget leaves with it")

		item, err := svc.GetCommon(ctx, businessScopes(fixture, f)[f.team], f.team)
		require.NoError(t, err)
		require.NotContains(t, string(item.Representation), string(f.user), "the team projection is published again")
		events, _ := feedSince(t, store, cursor)
		require.Contains(t, eventTypes(events), "team.updated.v1")
		require.Contains(t, eventTypes(events), "quota.deleted.v1")
	})
}

// TestPurgeCrossedScopes purges an organization and one of its members, both
// deleted, in either order.
func TestPurgeCrossedScopes(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		store := lifecycleStore(t, db)
		for _, memberFirst := range []bool{true, false} {
			fixture := newOwnershipFixture(t, base)
			org := confirmDeletion(t, db, store, orgScope(testTenantID), string(fixture.org))
			member := confirmDeletion(t, db, store, memberScope(testTenantID), string(fixture.user))
			first, second := org, member
			if memberFirst {
				first, second = member, org
			}
			require.NoError(t, store.PurgeDeletion(ctx, first))
			require.NoError(t, store.PurgeDeletion(ctx, second))
			require.False(t, exists(t, db, "organizations", string(fixture.org)))
			require.False(t, exists(t, db, "users", string(fixture.user)))
			require.Zero(t, countRows(t, db, "memberships", "user_id = ? OR org_id = ?", string(fixture.user), string(fixture.org)))
			due, err := store.DueDeletions(ctx, 10)
			require.NoError(t, err)
			require.Empty(t, due)
		}
	})
}

// TestPurgeConcurrently purges an organization and one of its members, both
// deleted, at the same time: the purges of one tenant queue, and both end.
func TestPurgeConcurrently(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		store := lifecycleStore(t, db, xologorm.WithPurgeBatch(1))
		deletions := []model.Deletion{
			confirmDeletion(t, db, store, orgScope(testTenantID), string(fixture.org)),
			confirmDeletion(t, db, store, memberScope(testTenantID), string(fixture.user)),
		}
		results := make(chan error, len(deletions))
		for _, d := range deletions {
			go func() { results <- store.PurgeDeletion(context.Background(), d) }()
		}
		for range deletions {
			require.NoError(t, <-results)
		}
		require.False(t, exists(t, db, "organizations", string(fixture.org)))
		require.False(t, exists(t, db, "users", string(fixture.user)))
		for _, d := range deletions {
			purged, err := store.ReadDeletion(t.Context(), model.CommonScope{Family: d.Family, TenantID: string(testTenantID)}, d.ResourceID)
			require.NoError(t, err)
			require.NotNil(t, purged.PurgedAt)
		}
	})
}

// TestPurgeResumes interrupts a purge between two batches: the next one
// resumes it, and the scope ends up empty.
func TestPurgeResumes(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		const records = 2_500
		rows := make([]xologorm.UsageRecord, 0, records)
		for range records {
			rows = append(rows, xologorm.UsageRecord{ID: uuid.NewString(), CreatedAt: time.Now(), UserID: string(fixture.user), OrgID: string(fixture.org), Currency: "EUR"})
		}
		require.NoError(t, db.CreateInBatches(rows, 500).Error)
		store := lifecycleStore(t, db, xologorm.WithPurgeBatch(100))
		d := confirmDeletion(t, db, store, orgScope(testTenantID), string(fixture.org))

		// The export holds every row, one at a time.
		_, summary := exportDeletion(t, store, orgScope(testTenantID), string(fixture.org))
		require.Equal(t, records, summary.Tables["usage_records"])

		interrupted := errors.New("interrupted")
		batches := 0
		require.NoError(t, db.Callback().Raw().After("gorm:raw").Register("test:interrupt", func(tx *gormpkg.DB) {
			if strings.HasPrefix(tx.Statement.SQL.String(), "DELETE FROM usage_records") {
				if batches++; batches == 3 {
					_ = tx.AddError(interrupted)
				}
			}
		}))
		t.Cleanup(func() { _ = db.Callback().Raw().Remove("test:interrupt") })
		require.ErrorIs(t, store.PurgeDeletion(t.Context(), d), interrupted)
		left := countRows(t, db, "usage_records", "org_id = ?", string(fixture.org))
		require.Equal(t, int64(records-200), left, "the two committed batches stay purged")
		stopped, err := store.ReadDeletion(t.Context(), orgScope(testTenantID), string(fixture.org))
		require.NoError(t, err)
		require.Nil(t, stopped.PurgedAt)
		require.Equal(t, 1, stopped.Attempts)
		require.Equal(t, model.DeletionPurgeFailed, stopped.Diagnostic)

		require.NoError(t, store.PurgeDeletion(t.Context(), d))
		require.Zero(t, countRows(t, db, "usage_records", "org_id = ?", string(fixture.org)))
		require.False(t, exists(t, db, "organizations", string(fixture.org)))
	})
}

// TestFreezeRefusesPlatformAdmins never freezes a platform administrator,
// nor a tenant holding one: the purge would remove the account.
func TestFreezeRefusesPlatformAdmins(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		store := lifecycleStore(t, db)
		tenant := newWebhookTenant(t, base, "administered")
		admin := model.NewUser(tenant, "oidc", uuid.NewString(), "root@example.test", "Root", true, model.PlatformRoleUser, model.PlatformRoleAdmin)
		require.NoError(t, base.SaveUser(ctx, admin))

		_, err := store.FreezeResource(ctx, memberScope(tenant), string(admin.ID()), model.MatchCondition{})
		require.ErrorIs(t, err, port.ErrPlatformAdminProtected)
		_, err = store.FreezeResource(ctx, tenantScope, string(tenant), model.MatchCondition{})
		require.ErrorIs(t, err, port.ErrPlatformAdminProtected)
		require.Zero(t, countRows(t, db, "resource_deletions", "tenant_id = ?", string(tenant)))
	})
}

// TestPurgeLocksOnlyItsScope: on PostgreSQL, a write in progress in another
// tenant never holds the purge of a tenant.
func TestPurgeLocksOnlyItsScope(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		if db.Dialector.Name() != "postgres" {
			t.Skip("SQLite serializes every writer")
		}
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		store := lifecycleStore(t, db)
		purged := newWebhookTenant(t, base, "purged")
		org := model.NewOrganization(purged, "inside", "Inside", "")
		require.NoError(t, base.CreateOrg(ctx, org))
		require.NoError(t, base.RecordUsage(ctx, model.NewUsageRecord("", "", org.ID(), "", "", "", "fast", "", 1, 1, 2, 10, "EUR", model.CostSourceComputed, "")))
		d := confirmDeletion(t, db, store, tenantScope, string(purged))

		// Another tenant writes, and does not commit yet: its guard holds its
		// own organization FOR KEY SHARE.
		writing := db.Begin()
		require.NoError(t, writing.Create(&xologorm.UsageRecord{ID: uuid.NewString(), CreatedAt: time.Now(), UserID: string(fixture.user), OrgID: string(fixture.org), Currency: "EUR"}).Error)

		done := make(chan error, 1)
		go func() { done <- store.PurgeDeletion(context.Background(), d) }()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("a write of another tenant blocked the purge")
		}
		require.NoError(t, writing.Commit().Error)
		require.False(t, exists(t, db, "tenants", string(purged)))
		require.Equal(t, int64(1), countRows(t, db, "usage_records", "org_id = ?", string(fixture.org)))
	})
}

// TestPurgeHoldsOnlyItsScope: on PostgreSQL, a purge in progress, its
// transaction open after removing rows of its tenant, never holds a write of
// another tenant. Its own tenant stays refused.
func TestPurgeHoldsOnlyItsScope(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		if db.Dialector.Name() != "postgres" {
			t.Skip("SQLite serializes every writer")
		}
		ctx := t.Context()
		base := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, base)
		store := lifecycleStore(t, db)
		purged := newWebhookTenant(t, base, "purged")
		org := model.NewOrganization(purged, "inside", "Inside", "")
		require.NoError(t, base.CreateOrg(ctx, org))
		require.NoError(t, base.RecordUsage(ctx, model.NewUsageRecord("", "", org.ID(), "", "", "", "fast", "", 1, 1, 2, 10, "EUR", model.CostSourceComputed, "")))
		d := confirmDeletion(t, db, store, tenantScope, string(purged))

		// The purge stops inside its transaction, right after removing the
		// usage of its tenant, until released.
		paused, release := make(chan struct{}), make(chan struct{})
		var once bool
		require.NoError(t, db.Callback().Raw().After("gorm:raw").Register("test:pause", func(tx *gormpkg.DB) {
			if !once && strings.HasPrefix(tx.Statement.SQL.String(), "DELETE FROM usage_records") {
				once = true
				close(paused)
				<-release
			}
		}))
		t.Cleanup(func() { _ = db.Callback().Raw().Remove("test:pause") })
		done := make(chan error, 1)
		go func() { done <- store.PurgeDeletion(context.Background(), d) }()
		select {
		case <-paused:
		case <-time.After(10 * time.Second):
			t.Fatal("the purge never reached its rows")
		}

		other := make(chan error, 1)
		go func() {
			other <- base.RecordUsage(context.Background(), model.NewUsageRecord(fixture.user, "", fixture.org, "", "", "", "fast", "", 1, 1, 2, 10, "EUR", model.CostSourceComputed, ""))
		}()
		select {
		case err := <-other:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("a purge in progress blocked another tenant")
		}
		// The purge skips the guards only in its own transaction.
		require.ErrorIs(t, base.RecordUsage(context.Background(), model.NewUsageRecord("", "", org.ID(), "", "", "", "fast", "", 1, 1, 2, 10, "EUR", model.CostSourceComputed, "")),
			port.ErrResourceDeleted)
		close(release)
		require.NoError(t, <-done)
		require.Equal(t, int64(1), countRows(t, db, "usage_records", "org_id = ?", string(fixture.org)))
		require.False(t, exists(t, db, "tenants", string(purged)))
	})
}
