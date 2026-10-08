package gorm_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
	gormpkg "gorm.io/gorm"
)

const businessSecretKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func businessService(store *xologorm.Store) *service.ProvisioningService {
	return service.NewProvisioningService(store, store, store, store,
		service.WithProvisioningTransaction(store), service.WithProvisioningReader(store),
		service.WithMultiTenant(true), service.WithSecretKey(businessSecretKey))
}

var noCondition = model.MatchCondition{}

func providerSettings(name string, key *string) model.ProviderSettings {
	return model.ProviderSettings{Name: name, Type: "openai", BaseURL: "https://llm.example.test/v1", Active: true,
		Currency: "EUR", BillingMode: model.BillingModePayg, APIKey: key}
}

func alertSettings(owner model.UserID, threshold float64) model.AlertSettings {
	return model.AlertSettings{Name: "Errors", Scope: model.AlertScopePersonal, OwnerID: string(owner), Query: `{type="llm.request.failed"}`,
		Aggregation: model.AggregationCount, WindowSeconds: 300, Comparator: model.ComparatorGT, Threshold: threshold, Enabled: true}
}

func ptrTo[T any](v T) *T { return &v }

// businessFixture provisions one resource of each business family in the
// organization of fixture.
type businessFixture struct {
	provider, role, app, quota, alert string
	llm                               model.LLMModelID
}

func newBusinessFixture(t *testing.T, store *xologorm.Store, svc *service.ProvisioningService, fixture ownershipFixture) businessFixture {
	t.Helper()
	ctx := t.Context()
	f := businessFixture{provider: uuid.NewString(), role: uuid.NewString(), app: uuid.NewString(), quota: uuid.NewString(), alert: uuid.NewString()}
	_, err := svc.PutProvider(ctx, testTenantID, fixture.org, f.provider, noCondition, providerSettings("OpenAI", ptrTo("sk-first-secret")))
	require.NoError(t, err)
	llm := model.NewLLMModel(model.ProviderID(f.provider), fixture.org, "gpt-"+uuid.NewString()[:8], "gpt-4o", "", 0, 0)
	require.NoError(t, store.CreateLLMModel(ctx, llm))
	f.llm = llm.ID()
	_, err = svc.PutCustomRole(ctx, testTenantID, fixture.org, f.role, noCondition, model.CustomRoleSettings{
		Name: "Analyst", Permissions: []string{string(rbac.PermUsageRead)},
		ModelGrants: []model.ModelGrantSettings{{ModelID: string(llm.ID()), Kind: rbac.ModelKindLLM}},
	})
	require.NoError(t, err)
	_, err = svc.PutApplication(ctx, testTenantID, fixture.org, f.app, noCondition, model.ApplicationSettings{Name: "Batch", Active: true, RoleIDs: []string{f.role}})
	require.NoError(t, err)
	_, err = svc.PutQuota(ctx, testTenantID, f.quota, noCondition, model.QuotaSettings{Scope: model.QuotaScopeApplication, ScopeID: f.app, Currency: "EUR", MonthlyBudget: ptrTo(int64(1000))})
	require.NoError(t, err)
	_, err = svc.PutAlert(ctx, testTenantID, fixture.org, f.alert, noCondition, alertSettings(fixture.user, 3))
	require.NoError(t, err)
	return f
}

func businessScopes(fixture ownershipFixture, f businessFixture) map[string]model.CommonScope {
	org := func(family string) model.CommonScope {
		return model.CommonScope{Family: family, TenantID: string(testTenantID), OrganizationID: string(fixture.org)}
	}
	return map[string]model.CommonScope{
		f.provider: org(model.FamilyProvider), f.role: org(model.FamilyCustomRole), f.app: org(model.FamilyApplication),
		f.alert: org(model.FamilyAlert), f.quota: {Family: model.FamilyQuota, TenantID: string(testTenantID)},
	}
}

// TestBusinessRoundTrip writes one resource of each family, reads it back
// with its ETag, and replays every PUT: a replay changes nothing.
func TestBusinessRoundTrip(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		svc := businessService(store)
		fixture := newOwnershipFixture(t, store)
		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		f := newBusinessFixture(t, store, svc, fixture)

		events, _ := feedSince(t, store, cursor)
		require.Equal(t, []string{
			"provider.created.v1", "custom_role.created.v1", "application.created.v1", "quota.created.v1", "alert.created.v1",
		}, eventTypes(events))
		require.Equal(t, f.app, events[2].Data.Key.ResourceID)
		require.Equal(t, string(fixture.org), events[2].Data.Key.OrganizationID)

		items := map[string]model.CommonItem{}
		for key, scope := range businessScopes(fixture, f) {
			item, err := svc.GetCommon(ctx, scope, key)
			require.NoError(t, err, scope.Family)
			items[key] = item
			page, err := svc.ListCommon(ctx, scope, "", 100)
			require.NoError(t, err)
			require.Len(t, page.Items, 1, scope.Family)
		}
		var role map[string]any
		require.NoError(t, json.Unmarshal(items[f.role].Representation, &role))
		require.Equal(t, []any{map[string]any{"model_id": string(f.llm), "kind": rbac.ModelKindLLM}}, role["model_grants"])
		var app map[string]any
		require.NoError(t, json.Unmarshal(items[f.app].Representation, &app))
		require.Equal(t, []any{f.role}, app["role_ids"])

		// Replays keep every ETag, write no audit and publish nothing.
		audits, published := auditCount(t, db), eventCount(t, db)
		replayed := newBusinessReplay(t, svc, fixture, f)
		for key, item := range replayed {
			require.Equal(t, items[key].ETag, item.ETag, key)
		}
		require.Equal(t, audits, auditCount(t, db))
		require.Equal(t, published, eventCount(t, db))

		// A stale condition fails before any write.
		stale := ifMatch(t, `W/"1"`)
		_, err = svc.PutAlert(ctx, testTenantID, fixture.org, f.alert, stale, alertSettings(fixture.user, 9))
		require.ErrorIs(t, err, port.ErrPreconditionFailed)
		_, err = svc.PutAlert(ctx, testTenantID, fixture.org, f.alert, ifMatch(t, items[f.alert].ETag), alertSettings(fixture.user, 9))
		require.NoError(t, err)
	})
}

func newBusinessReplay(t *testing.T, svc *service.ProvisioningService, fixture ownershipFixture, f businessFixture) map[string]model.CommonItem {
	t.Helper()
	ctx := t.Context()
	out := map[string]model.CommonItem{}
	var err error
	out[f.provider], err = svc.PutProvider(ctx, testTenantID, fixture.org, f.provider, noCondition, providerSettings("OpenAI", ptrTo("sk-first-secret")))
	require.NoError(t, err)
	out[f.role], err = svc.PutCustomRole(ctx, testTenantID, fixture.org, f.role, noCondition, model.CustomRoleSettings{
		Name: "Analyst", Permissions: []string{string(rbac.PermUsageRead), string(rbac.PermUsageRead)},
		ModelGrants: []model.ModelGrantSettings{{ModelID: string(f.llm), Kind: rbac.ModelKindLLM}},
	})
	require.NoError(t, err)
	out[f.app], err = svc.PutApplication(ctx, testTenantID, fixture.org, f.app, noCondition, model.ApplicationSettings{Name: "Batch", Active: true, RoleIDs: []string{f.role, f.role}})
	require.NoError(t, err)
	out[f.quota], err = svc.PutQuota(ctx, testTenantID, f.quota, noCondition, model.QuotaSettings{Scope: model.QuotaScopeApplication, ScopeID: f.app, Currency: "EUR", MonthlyBudget: ptrTo(int64(1000))})
	require.NoError(t, err)
	out[f.alert], err = svc.PutAlert(ctx, testTenantID, fixture.org, f.alert, noCondition, alertSettings(fixture.user, 3))
	require.NoError(t, err)
	return out
}

// TestBusinessSecrets keeps the provider key out of every read, projection,
// event and audit; a rotation is audited by its fingerprint only.
func TestBusinessSecrets(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		svc := businessService(store)
		fixture := newOwnershipFixture(t, store)
		key := uuid.NewString()

		_, err := svc.PutProvider(ctx, testTenantID, fixture.org, key, noCondition, providerSettings("OpenAI", nil))
		require.ErrorIs(t, err, port.ErrInvalid, "a new provider needs a key")
		created, err := svc.PutProvider(ctx, testTenantID, fixture.org, key, noCondition, providerSettings("OpenAI", ptrTo("sk-first-secret")))
		require.NoError(t, err)
		stored, err := store.GetProviderByID(ctx, model.ProviderID(key))
		require.NoError(t, err)
		firstCiphertext := stored.APIKey()

		// Omitted, the key is kept.
		kept, err := svc.PutProvider(ctx, testTenantID, fixture.org, key, noCondition, providerSettings("OpenAI", nil))
		require.NoError(t, err)
		require.Equal(t, created.ETag, kept.ETag)
		stored, err = store.GetProviderByID(ctx, model.ProviderID(key))
		require.NoError(t, err)
		require.Equal(t, firstCiphertext, stored.APIKey())

		// A rotation changes no representation, but is audited.
		audits := auditCount(t, db)
		rotated, err := svc.PutProvider(ctx, testTenantID, fixture.org, key, noCondition, providerSettings("OpenAI", ptrTo("sk-second-secret")))
		require.NoError(t, err)
		require.Equal(t, created.ETag, rotated.ETag)
		require.Equal(t, audits+1, auditCount(t, db))
		stored, err = store.GetProviderByID(ctx, model.ProviderID(key))
		require.NoError(t, err)
		require.NotEqual(t, firstCiphertext, stored.APIKey())

		// A change of the representation without key keeps the key.
		renamed, err := svc.PutProvider(ctx, testTenantID, fixture.org, key, noCondition, providerSettings("Renamed", nil))
		require.NoError(t, err)
		require.NotEqual(t, created.ETag, renamed.ETag)
		kept2, err := store.GetProviderByID(ctx, model.ProviderID(key))
		require.NoError(t, err)
		require.Equal(t, stored.APIKey(), kept2.APIKey())

		var texts []string
		for _, table := range []struct{ name, column string }{
			{"provisioning_projections", "representation"}, {"provisioning_events", "payload"}, {"mutation_audits", "before"}, {"mutation_audits", "after"},
		} {
			var values []string
			require.NoError(t, db.Table(table.name).Pluck(table.column, &values).Error)
			texts = append(texts, values...)
		}
		all := strings.Join(texts, "\n")
		for _, secret := range []string{"sk-first-secret", "sk-second-secret", firstCiphertext, stored.APIKey(), "api_key\""} {
			require.NotContains(t, all, secret)
		}
		require.Contains(t, all, "api_key_fingerprint")
	})
}

// TestBusinessParents refuses every reference outside the organization or
// tenant, and reports a foreign resource like a missing one.
func TestBusinessParents(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		svc := businessService(store)
		fixture := newOwnershipFixture(t, store)
		other := newOwnershipFixture(t, store)
		f := newBusinessFixture(t, store, svc, other)

		foreignGrant := model.CustomRoleSettings{Name: "Thief", Permissions: []string{},
			ModelGrants: []model.ModelGrantSettings{{ModelID: string(f.llm), Kind: rbac.ModelKindLLM}}}
		_, err := svc.PutCustomRole(ctx, testTenantID, fixture.org, uuid.NewString(), noCondition, foreignGrant)
		require.ErrorIs(t, err, port.ErrParentNotFound)

		_, err = svc.PutApplication(ctx, testTenantID, fixture.org, uuid.NewString(), noCondition, model.ApplicationSettings{Name: "Thief", RoleIDs: []string{f.role}})
		require.ErrorIs(t, err, port.ErrParentNotFound)

		_, err = svc.PutAlert(ctx, testTenantID, fixture.org, uuid.NewString(), noCondition, alertSettings(other.user, 1))
		require.ErrorIs(t, err, port.ErrParentNotFound, "the owner is no member of the organization")

		foreignTenant := model.NewTenant("foreign", "Foreign", "")
		require.NoError(t, store.CreateTenant(ctx, foreignTenant))
		stranger := model.NewUser(foreignTenant.ID(), "oidc", uuid.NewString(), "", "", true, model.PlatformRoleUser)
		require.NoError(t, store.SaveUser(ctx, stranger))
		_, err = svc.PutQuota(ctx, testTenantID, uuid.NewString(), noCondition, model.QuotaSettings{Scope: model.QuotaScopeUser, ScopeID: string(stranger.ID()), Currency: "EUR"})
		require.ErrorIs(t, err, port.ErrParentNotFound)

		// A resource of another organization keeps its key: not found here.
		_, err = svc.PutProvider(ctx, testTenantID, fixture.org, f.provider, noCondition, providerSettings("Thief", ptrTo("sk")))
		require.ErrorIs(t, err, port.ErrNotFound)
		_, err = svc.GetCommon(ctx, model.CommonScope{Family: model.FamilyProvider, TenantID: string(testTenantID), OrganizationID: string(fixture.org)}, f.provider)
		require.ErrorIs(t, err, port.ErrNotFound)
		_, err = svc.PutQuota(ctx, foreignTenant.ID(), f.quota, noCondition, model.QuotaSettings{Scope: model.QuotaScopeUser, ScopeID: string(stranger.ID()), Currency: "EUR"})
		require.ErrorIs(t, err, port.ErrNotFound)

		// Only a UUID creates a resource.
		_, err = svc.PutProvider(ctx, testTenantID, fixture.org, string(model.NewProviderID()), noCondition, providerSettings("Local", ptrTo("sk")))
		require.ErrorIs(t, err, port.ErrInvalid)
	})
}

// TestBusinessQuota keeps the spend recorded, the scope of a quota, and one
// quota per scope.
func TestBusinessQuota(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		svc := businessService(store)
		fixture := newOwnershipFixture(t, store)
		key := uuid.NewString()
		settings := model.QuotaSettings{Scope: model.QuotaScopeUser, ScopeID: string(fixture.user), Currency: "EUR", DailyBudget: ptrTo(int64(10))}
		_, err := svc.PutQuota(ctx, testTenantID, key, noCondition, settings)
		require.NoError(t, err)
		usage := xologorm.QuotaUsage{Scope: string(model.QuotaScopeUser), ScopeID: string(fixture.user), OrgID: string(fixture.org), Currency: "EUR", Day: "2026-10-08", Cost: 42}
		require.NoError(t, db.Create(&usage).Error)

		settings.DailyBudget = ptrTo(int64(20))
		_, err = svc.PutQuota(ctx, testTenantID, key, noCondition, settings)
		require.NoError(t, err)
		var cost int64
		require.NoError(t, db.Model(&xologorm.QuotaUsage{}).Where("scope_id = ?", string(fixture.user)).Pluck("cost", &cost).Error)
		require.EqualValues(t, 42, cost, "the spend is kept")

		moved := settings
		moved.Scope, moved.ScopeID = model.QuotaScopeOrg, string(fixture.org)
		_, err = svc.PutQuota(ctx, testTenantID, key, noCondition, moved)
		require.ErrorIs(t, err, port.ErrNotAllowed)
		_, err = svc.PutQuota(ctx, testTenantID, uuid.NewString(), noCondition, settings)
		require.ErrorIs(t, err, port.ErrAlreadyExists)
	})
}

// TestBusinessAlertState keeps the evaluation out of the contract: the
// evaluator publishes nothing, a replay keeps the state, a change resets it.
func TestBusinessAlertState(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		svc := businessService(store)
		fixture := newOwnershipFixture(t, store)
		key := uuid.NewString()
		_, err := svc.PutAlert(ctx, testTenantID, fixture.org, key, noCondition, alertSettings(fixture.user, 3))
		require.NoError(t, err)

		published := eventCount(t, db)
		now := time.Now()
		require.NoError(t, store.UpdateAlertState(ctx, model.AlertID(key), model.AlertStateFiring, &now, &now))
		require.Equal(t, published, eventCount(t, db))

		_, err = svc.PutAlert(ctx, testTenantID, fixture.org, key, noCondition, alertSettings(fixture.user, 3))
		require.NoError(t, err)
		alert, err := store.GetAlertByID(ctx, model.AlertID(key))
		require.NoError(t, err)
		require.Equal(t, model.AlertStateFiring, alert.State())

		_, err = svc.PutAlert(ctx, testTenantID, fixture.org, key, noCondition, alertSettings(fixture.user, 5))
		require.NoError(t, err)
		alert, err = store.GetAlertByID(ctx, model.AlertID(key))
		require.NoError(t, err)
		require.Equal(t, model.AlertStateOK, alert.State())

		changed := alertSettings(fixture.user, 5)
		changed.Scope = model.AlertScopeOrg
		_, err = svc.PutAlert(ctx, testTenantID, fixture.org, key, noCondition, changed)
		require.ErrorIs(t, err, port.ErrNotAllowed)
	})
}

// TestBusinessCascades publishes the removal of the business resources of a
// deleted organization, and refuses it when the control plane owns them.
func TestBusinessCascades(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		base := newSeededStore(t, db)
		svc := businessService(base)
		fixture := newOwnershipFixture(t, base)
		newBusinessFixture(t, base, svc, fixture)

		providers := ownedStore(t, db, model.OwnershipPolicy{model.FamilyProvider: model.OwnerControlPlane})
		require.ErrorIs(t, providers.DeleteOrg(ctx, fixture.org), port.ErrOwnershipDenied)
		var count int64
		require.NoError(t, db.Table("providers").Where("org_id = ?", string(fixture.org)).Count(&count).Error)
		require.EqualValues(t, 1, count)

		cursor, err := base.CaptureEventCursor(ctx)
		require.NoError(t, err)
		require.NoError(t, base.DeleteOrg(ctx, fixture.org))
		events, _ := feedSince(t, base, cursor)
		deleted := map[string]bool{}
		for _, event := range events {
			deleted[event.Type] = true
		}
		for _, family := range model.BusinessFamilies {
			require.True(t, deleted[model.CommonEventType(family, model.CommonEventDeleted)], family)
		}
	})
}

// TestBusinessLocalWrites publishes the writes of the web UI too, and lets
// the policy refuse them.
func TestBusinessLocalWrites(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, store)
		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)

		app := model.NewApplication(fixture.org, "Local", "", true)
		require.NoError(t, store.CreateApplication(ctx, app))
		require.NoError(t, store.SetQuota(ctx, model.NewQuota(model.QuotaScopeOrg, string(fixture.org), "EUR", nil, ptrTo(int64(5)), nil)))
		require.NoError(t, store.SetQuota(ctx, model.NewQuota(model.QuotaScopeOrg, string(fixture.org), "EUR", nil, ptrTo(int64(6)), nil)))
		events, _ := feedSince(t, store, cursor)
		require.Equal(t, []string{"application.created.v1", "quota.created.v1", "quota.updated.v1"}, eventTypes(events))

		owned := ownedStore(t, db, model.OwnershipPolicy{model.FamilyApplication: model.OwnerControlPlane})
		require.ErrorIs(t, owned.UpdateApplication(ctx, model.UpdateApplication(app, model.WithApplicationName("Renamed"))), port.ErrOwnershipDenied)
		require.NoError(t, owned.UpdateApplication(model.WithWriteAuthority(ctx, model.OwnerControlPlane), model.UpdateApplication(app, model.WithApplicationName("Renamed"))))
	})
}

// TestBusinessBackfill projects the business resources written before the
// migration, under their local identifiers, without publishing anything.
func TestBusinessBackfill(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, store)
		legacy := xologorm.Provider{ID: string(model.NewProviderID()), OrgID: string(fixture.org), Name: "Legacy", Type: "openai", APIKey: "ciphertext", Active: 1, Currency: "EUR", BillingMode: "payg"}
		orphan := xologorm.Alert{ID: string(model.NewAlertID()), OrgID: string(model.NewOrgID()), Name: "Orphan", Scope: "org"}
		require.NoError(t, db.Create(&legacy).Error)
		require.NoError(t, db.Create(&orphan).Error)
		var before int64
		require.NoError(t, db.Model(&xologorm.ProvisioningProjection{}).Where("family = ?", model.FamilyProvider).Count(&before).Error)
		require.Zero(t, before)

		events := eventCount(t, db)
		require.NoError(t, db.Transaction(func(tx *gormpkg.DB) error { return xologorm.MigrateBusinessProjections(tx) }))
		require.Equal(t, events, eventCount(t, db))
		item, err := store.ReadProjection(ctx, model.CommonScope{Family: model.FamilyProvider, TenantID: string(testTenantID), OrganizationID: string(fixture.org)}, legacy.ID)
		require.NoError(t, err)
		require.NotContains(t, string(item.Representation), "ciphertext")
		var alerts int64
		require.NoError(t, db.Model(&xologorm.ProvisioningProjection{}).Where("family = ?", model.FamilyAlert).Count(&alerts).Error)
		require.Zero(t, alerts, "an orphan is left out")
	})
}
