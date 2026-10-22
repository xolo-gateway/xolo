package gorm_test

import (
	"encoding/json"
	"fmt"
	"slices"
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

// pipelineGraph is a complete pipeline: a generator, a plugin node holding a
// threshold, a model and a sink.
func pipelineGraph(threshold float64) model.PipelineGraphSettings {
	return model.PipelineGraphSettings{
		Nodes: []model.PipelineNodeSettings{
			{ID: "gen", Type: model.NodeTypeGenerator, Data: json.RawMessage(`{}`)},
			{ID: "guard", Type: model.NodeTypePlugin, Position: model.NodePosition{X: 200}, Data: json.RawMessage(fmt.Sprintf(`{"pluginName":"guard","config":{"threshold":%v}}`, threshold))},
			{ID: "model", Type: model.NodeTypeModel, Data: json.RawMessage(`{"passthrough":true}`)},
			{ID: "sink", Type: model.NodeTypeSink, Data: json.RawMessage(`{}`)},
		},
		Edges: []model.PipelineEdgeSettings{
			{ID: "gen-guard", Source: "gen", SourcePort: "request", Target: "guard", TargetPort: "request"},
			{ID: "guard-model", Source: "guard", SourcePort: "request", Target: "model", TargetPort: "request"},
			{ID: "model-sink", Source: "model", SourcePort: "response", Target: "sink", TargetPort: "response"},
		},
	}
}

func virtualModelSettings(name string, threshold float64) model.VirtualModelSettings {
	return model.VirtualModelSettings{Name: name, Description: "", Graph: pipelineGraph(threshold)}
}

// middlewareSettings wraps the given model with a pipeline whose plugin node
// does not collide with the one of the fixture virtual model.
func middlewareSettings(target model.LLMModelID) model.MiddlewareSettings {
	graph := pipelineGraph(0.5)
	graph.Nodes[1].ID = "mw-guard"
	graph.Edges[0].Target, graph.Edges[1].Source = "mw-guard", "mw-guard"
	return model.MiddlewareSettings{Name: "Guard", Enabled: true, Priority: 1,
		Targets: []model.ModelRef{{Kind: model.ModelRefKindLLM, ID: string(target)}}, Graph: graph}
}

// businessFixture provisions one resource of each business family in the
// organization of fixture.
type businessFixture struct {
	provider, role, team, app, quota, alert string
	virtualModel, middleware                string
	llm                                     model.LLMModelID
	user                                    model.UserID
}

func newBusinessFixture(t *testing.T, store *xologorm.Store, svc *service.ProvisioningService, fixture ownershipFixture) businessFixture {
	t.Helper()
	ctx := t.Context()
	f := businessFixture{provider: uuid.NewString(), role: uuid.NewString(), team: uuid.NewString(), app: uuid.NewString(), quota: uuid.NewString(), alert: uuid.NewString(),
		virtualModel: uuid.NewString(), middleware: uuid.NewString(), user: fixture.user}
	_, err := svc.PutProvider(ctx, testTenantID, fixture.org, f.provider, noCondition, providerSettings("OpenAI", ptrTo("sk-first-secret")))
	require.NoError(t, err)
	llm := model.NewLLMModel(model.ProviderID(f.provider), fixture.org, "gpt-"+uuid.NewString()[:8], "gpt-4o", "", 0, 0)
	require.NoError(t, store.CreateLLMModel(ctx, llm))
	f.llm = llm.ID()
	_, err = svc.PutVirtualModel(ctx, testTenantID, fixture.org, f.virtualModel, noCondition, virtualModelSettings("router", 0.5))
	require.NoError(t, err)
	_, err = svc.PutMiddleware(ctx, testTenantID, fixture.org, f.middleware, noCondition, middlewareSettings(f.llm))
	require.NoError(t, err)
	_, err = svc.PutCustomRole(ctx, testTenantID, fixture.org, f.role, noCondition, model.CustomRoleSettings{
		Name: "Analyst", Permissions: []string{string(rbac.PermUsageRead)},
	})
	require.NoError(t, err)
	provisionTeam(t, svc, fixture.org, f)
	_, err = svc.PutApplication(ctx, testTenantID, fixture.org, f.app, noCondition, model.ApplicationSettings{Name: "Batch", Active: true, RoleIDs: []string{f.role}, TeamID: f.team})
	require.NoError(t, err)
	_, err = svc.PutQuota(ctx, testTenantID, f.quota, noCondition, model.QuotaSettings{Scope: model.QuotaScopeApplication, ScopeID: f.app, Currency: "EUR", MonthlyBudget: ptrTo(int64(1000))})
	require.NoError(t, err)
	_, err = svc.PutAlert(ctx, testTenantID, fixture.org, f.alert, noCondition, alertSettings(fixture.user, 3))
	require.NoError(t, err)
	return f
}

// teamResource is the common representation of the fixture team.
func teamResource() service.CommonResource {
	return service.CommonResource{Slug: "analysts", Name: "Analysts", Status: model.StatusActive}
}

// teamSettings grants the fixture model to the fixture team, whose fixture
// user holds the fixture role on the team.
func teamSettings(f businessFixture) model.TeamSettings {
	return model.TeamSettings{
		Description:   "",
		ModelGrants:   []model.ModelGrantSettings{{ModelID: string(f.llm), Kind: rbac.ModelKindLLM}},
		MiddlewareIDs: []string{},
		MemberRoles:   []model.TeamMemberSettings{{UserID: string(f.user), RoleIDs: []string{f.role}}},
	}
}

// provisionTeam creates the fixture team through the common contract, adds
// the fixture user to it, then writes its settings.
func provisionTeam(t *testing.T, svc *service.ProvisioningService, org model.OrgID, f businessFixture) {
	t.Helper()
	ctx := t.Context()
	_, err := svc.PutTeam(ctx, testTenantID, org, model.TeamID(f.team), teamResource())
	require.NoError(t, err)
	_, err = svc.PutTeamMembership(ctx, testTenantID, org, model.TeamID(f.team), f.user, model.StatusActive)
	require.NoError(t, err)
	_, err = svc.PutTeamSettings(ctx, testTenantID, org, f.team, noCondition, teamSettings(f))
	require.NoError(t, err)
}

func businessScopes(fixture ownershipFixture, f businessFixture) map[string]model.CommonScope {
	org := func(family string) model.CommonScope {
		return model.CommonScope{Family: family, TenantID: string(testTenantID), OrganizationID: string(fixture.org)}
	}
	return map[string]model.CommonScope{
		f.provider: org(model.FamilyProvider), f.role: org(model.FamilyCustomRole), f.team: org(model.FamilyTeamSettings), f.app: org(model.FamilyApplication),
		f.alert: org(model.FamilyAlert), f.quota: {Family: model.FamilyQuota, TenantID: string(testTenantID)},
		f.virtualModel: org(model.FamilyVirtualModel), f.middleware: org(model.FamilyMiddleware),
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
			"provider.created.v1", "virtual_model.created.v1", "middleware.created.v1", "custom_role.created.v1", "team.created.v1", "team_settings.created.v1",
			"team_membership.created.v1", "team_settings.updated.v1", "application.created.v1", "quota.created.v1", "alert.created.v1",
		}, eventTypes(events))
		require.Equal(t, model.CommonKey{TenantID: string(testTenantID), OrganizationID: string(fixture.org), ResourceID: f.virtualModel}, events[1].Data.Key)
		require.Equal(t, model.CommonKey{TenantID: string(testTenantID), OrganizationID: string(fixture.org), TeamID: f.team}, events[4].Data.Key)
		require.Equal(t, model.CommonKey{TenantID: string(testTenantID), OrganizationID: string(fixture.org), TeamID: f.team, MemberID: string(f.user)}, events[6].Data.Key)
		require.Equal(t, f.app, events[8].Data.Key.ResourceID)
		require.Equal(t, string(fixture.org), events[8].Data.Key.OrganizationID)

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
		require.NotContains(t, role, "model_grants", "a role grants no model")
		var team map[string]any
		require.NoError(t, json.Unmarshal(items[f.team].Representation, &team))
		require.Equal(t, []any{map[string]any{"model_id": string(f.llm), "kind": rbac.ModelKindLLM}}, team["model_grants"])
		require.Equal(t, []any{map[string]any{"user_id": string(f.user), "role_ids": []any{f.role}}}, team["member_roles"])
		require.NotContains(t, team, "slug", "the common team holds the slug")
		common, err := svc.GetCommon(ctx, model.CommonScope{Family: model.FamilyTeam, TenantID: string(testTenantID), OrganizationID: string(fixture.org)}, f.team)
		require.NoError(t, err)
		require.JSONEq(t, `{"slug":"analysts","name":"Analysts","status":"active"}`, string(common.Representation))
		membership, err := svc.GetCommon(ctx, model.CommonScope{Family: model.FamilyTeamMembership, TenantID: string(testTenantID), OrganizationID: string(fixture.org), TeamID: f.team}, string(f.user))
		require.NoError(t, err)
		require.JSONEq(t, `{"status":"active"}`, string(membership.Representation))
		var app map[string]any
		require.NoError(t, json.Unmarshal(items[f.app].Representation, &app))
		require.Equal(t, []any{f.role}, app["role_ids"])
		require.Equal(t, f.team, app["team_id"])

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
	})
	require.NoError(t, err)
	replayTeam := teamSettings(f)
	replayTeam.ModelGrants = append(replayTeam.ModelGrants, replayTeam.ModelGrants...)
	out[f.team], err = svc.PutTeamSettings(ctx, testTenantID, fixture.org, f.team, noCondition, replayTeam)
	require.NoError(t, err)
	out[f.app], err = svc.PutApplication(ctx, testTenantID, fixture.org, f.app, noCondition, model.ApplicationSettings{Name: "Batch", Active: true, RoleIDs: []string{f.role, f.role}, TeamID: f.team})
	require.NoError(t, err)
	out[f.quota], err = svc.PutQuota(ctx, testTenantID, f.quota, noCondition, model.QuotaSettings{Scope: model.QuotaScopeApplication, ScopeID: f.app, Currency: "EUR", MonthlyBudget: ptrTo(int64(1000))})
	require.NoError(t, err)
	out[f.alert], err = svc.PutAlert(ctx, testTenantID, fixture.org, f.alert, noCondition, alertSettings(fixture.user, 3))
	require.NoError(t, err)
	// The order of nodes, edges and targets carries no meaning.
	vm := virtualModelSettings("router", 0.5)
	slices.Reverse(vm.Graph.Nodes)
	slices.Reverse(vm.Graph.Edges)
	vm.Graph.Nodes[0].Data = json.RawMessage(`{ }`)
	out[f.virtualModel], err = svc.PutVirtualModel(ctx, testTenantID, fixture.org, f.virtualModel, noCondition, vm)
	require.NoError(t, err)
	mw := middlewareSettings(f.llm)
	mw.Targets = append(mw.Targets, mw.Targets...)
	out[f.middleware], err = svc.PutMiddleware(ctx, testTenantID, fixture.org, f.middleware, noCondition, mw)
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

		thieves := uuid.NewString()
		_, err := svc.PutTeam(ctx, testTenantID, fixture.org, model.TeamID(thieves), service.CommonResource{Slug: "thieves", Name: "Thieves", Status: model.StatusActive})
		require.NoError(t, err)
		foreignGrant := model.TeamSettings{ModelGrants: []model.ModelGrantSettings{{ModelID: string(f.llm), Kind: rbac.ModelKindLLM}},
			MiddlewareIDs: []string{}, MemberRoles: []model.TeamMemberSettings{}}
		_, err = svc.PutTeamSettings(ctx, testTenantID, fixture.org, thieves, noCondition, foreignGrant)
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

// TestTeamProvisioning covers the teams of the common contract and their
// settings: a slug checked when it changes, a global key, members of the
// organization only, statuses that suspend access, team budgets and their
// cascade.
func TestTeamProvisioning(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		svc := businessService(store)
		fixture := newOwnershipFixture(t, store)
		f := newBusinessFixture(t, store, svc, fixture)
		teamID := model.TeamID(f.team)

		_, err := svc.PutTeam(ctx, testTenantID, fixture.org, teamID, service.CommonResource{Slug: "Not a slug", Name: "Analysts", Status: model.StatusActive})
		require.ErrorIs(t, err, port.ErrInvalid, "a new slug is checked")
		_, err = svc.PutTeam(ctx, testTenantID, fixture.org, teamID, service.CommonResource{Slug: " RENAMED ", Name: "Analysts", Status: model.StatusActive})
		require.NoError(t, err)
		team, err := store.GetTeamByID(ctx, teamID)
		require.NoError(t, err)
		require.Equal(t, "renamed", team.Slug())
		require.Len(t, team.ModelGrants(), 1, "a common PUT keeps the settings")
		_, err = svc.PutTeam(ctx, testTenantID, fixture.org, teamID, teamResource())
		require.NoError(t, err, "and back")

		stranger := newOwnershipFixture(t, store)
		_, err = svc.PutTeam(ctx, testTenantID, stranger.org, teamID, teamResource())
		require.ErrorIs(t, err, port.ErrAlreadyExists, "a team key never moves to another organization")
		_, err = svc.PutTeamMembership(ctx, testTenantID, fixture.org, teamID, stranger.user, model.StatusActive)
		require.ErrorIs(t, err, port.ErrParentNotFound, "a team member belongs to the organization")
		_, err = svc.PutTeamMembership(ctx, testTenantID, fixture.org, model.TeamID(uuid.NewString()), f.user, model.StatusActive)
		require.ErrorIs(t, err, port.ErrParentNotFound, "a team member needs its team")

		withStranger := teamSettings(f)
		withStranger.MemberRoles = append(withStranger.MemberRoles, model.TeamMemberSettings{UserID: string(stranger.user), RoleIDs: []string{f.role}})
		_, err = svc.PutTeamSettings(ctx, testTenantID, fixture.org, f.team, noCondition, withStranger)
		require.ErrorIs(t, err, port.ErrParentNotFound, "only team members hold team roles")
		twice := teamSettings(f)
		twice.MemberRoles = append(twice.MemberRoles, twice.MemberRoles[0])
		_, err = svc.PutTeamSettings(ctx, testTenantID, fixture.org, f.team, noCondition, twice)
		require.ErrorIs(t, err, port.ErrInvalid)
		roles, err := store.ListOrgRoles(ctx, fixture.org)
		require.NoError(t, err)
		builtin := slices.IndexFunc(roles, func(r model.Role) bool { return r.Builtin() })
		require.NotEqual(t, -1, builtin)
		withBuiltin := teamSettings(f)
		withBuiltin.MemberRoles[0].RoleIDs = []string{string(roles[builtin].ID())}
		_, err = svc.PutTeamSettings(ctx, testTenantID, fixture.org, f.team, noCondition, withBuiltin)
		require.ErrorIs(t, err, port.ErrInvalid, "a builtin role is not held on a team")
		_, err = svc.PutTeamSettings(ctx, testTenantID, fixture.org, uuid.NewString(), noCondition, teamSettings(f))
		require.ErrorIs(t, err, port.ErrParentNotFound, "settings exist with their team only")

		// A suspended team member, or a suspended team, grants nothing; both
		// keep their members, roles and grants.
		inTeam := func() bool {
			t.Helper()
			perms, err := store.ResolveEffectivePermissions(ctx, f.user, fixture.org)
			require.NoError(t, err)
			return perms.InTeam(f.team)
		}
		require.True(t, inTeam())
		_, err = svc.PutTeamMembership(ctx, testTenantID, fixture.org, teamID, f.user, model.StatusSuspended)
		require.NoError(t, err)
		require.False(t, inTeam())
		members, err := store.ListTeamMembers(ctx, teamID)
		require.NoError(t, err)
		require.Len(t, members, 1)
		require.Equal(t, model.StatusSuspended, members[0].Status())
		require.Equal(t, []model.RoleID{model.RoleID(f.role)}, members[0].RoleIDs())
		_, err = svc.PutTeamMembership(ctx, testTenantID, fixture.org, teamID, f.user, model.StatusActive)
		require.NoError(t, err)
		require.True(t, inTeam())
		_, err = svc.PutTeam(ctx, testTenantID, fixture.org, teamID, service.CommonResource{Slug: "analysts", Name: "Analysts", Status: model.StatusSuspended})
		require.NoError(t, err)
		require.False(t, inTeam())
		_, err = svc.PutTeam(ctx, testTenantID, fixture.org, teamID, teamResource())
		require.NoError(t, err)

		// A model whose proxy name starts with a slug keeps the slug free.
		llm := model.NewLLMModel(model.ProviderID(f.provider), fixture.org, "ops/llama", "llama", "", 0, 0)
		require.NoError(t, store.CreateLLMModel(ctx, llm))
		_, err = svc.PutTeam(ctx, testTenantID, fixture.org, model.TeamID(uuid.NewString()), service.CommonResource{Slug: "ops", Name: "Ops", Status: model.StatusActive})
		require.ErrorIs(t, err, port.ErrTeamNameConflict)

		// Team budgets: the organization splits its envelope among teams.
		_, err = svc.PutQuota(ctx, testTenantID, uuid.NewString(), noCondition, model.QuotaSettings{
			Scope: model.QuotaScopeOrg, ScopeID: string(fixture.org), Currency: "EUR", MonthlyBudget: ptrTo(int64(1000)), Cascade: model.QuotaCascadeSplit,
		})
		require.NoError(t, err)
		teamQuota := model.QuotaSettings{Scope: model.QuotaScopeTeam, ScopeID: f.team, Currency: "EUR", MonthlyBudget: ptrTo(int64(1500))}
		_, err = svc.PutQuota(ctx, testTenantID, uuid.NewString(), noCondition, teamQuota)
		var violation *service.CascadeViolation
		require.ErrorAs(t, err, &violation)
		teamQuota.MonthlyBudget = ptrTo(int64(800))
		teamQuotaKey := uuid.NewString()
		item, err := svc.PutQuota(ctx, testTenantID, teamQuotaKey, noCondition, teamQuota)
		require.NoError(t, err)
		var rep map[string]any
		require.NoError(t, json.Unmarshal(item.Representation, &rep))
		require.Equal(t, "free", rep["cascade"], "a cascade defaults to free")

		// A member budget needs a member of the team.
		_, err = svc.PutQuota(ctx, testTenantID, uuid.NewString(), noCondition, model.QuotaSettings{
			Scope: model.QuotaScopeTeamMember, ScopeID: string(model.TeamMemberScopeID(model.TeamID(f.team), stranger.user)), Currency: "EUR",
		})
		require.ErrorIs(t, err, port.ErrParentNotFound)
		_, err = svc.PutQuota(ctx, testTenantID, uuid.NewString(), noCondition, model.QuotaSettings{
			Scope: model.QuotaScopeTeamMember, ScopeID: model.TeamMemberScopeID(model.TeamID(f.team), f.user), Currency: "EUR", MonthlyBudget: ptrTo(int64(100)),
			Cascade: model.QuotaCascadeSplit,
		})
		require.ErrorIs(t, err, port.ErrInvalid, "a member budget has no children to constrain")

		// The bound application keeps the team from going.
		err = svc.DeleteTeam(ctx, testTenantID, fixture.org, teamID, noCondition)
		require.ErrorIs(t, err, port.ErrNotAllowed)

		// Removing a member drops it from the team; removing it again changes
		// nothing.
		require.NoError(t, svc.DeleteTeamMembership(ctx, testTenantID, fixture.org, teamID, f.user, noCondition))
		require.NoError(t, svc.DeleteTeamMembership(ctx, testTenantID, fixture.org, teamID, f.user, noCondition))
		members, err = store.ListTeamMembers(ctx, teamID)
		require.NoError(t, err)
		require.Empty(t, members)

		// A member created suspended is written suspended at once: it is
		// never active, and its creation is the only event.
		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		_, err = svc.PutTeamMembership(ctx, testTenantID, fixture.org, teamID, f.user, model.StatusSuspended)
		require.NoError(t, err)
		members, err = store.ListTeamMembers(ctx, teamID)
		require.NoError(t, err)
		require.Len(t, members, 1)
		require.Equal(t, model.StatusSuspended, members[0].Status())
		events, _ := feedSince(t, store, cursor)
		require.Equal(t, []string{"team_membership.created.v1"}, eventTypes(events))
		var audits []xologorm.MutationAudit
		require.NoError(t, db.Where("resource = ?", "team_member").Order("created_at").Find(&audits).Error)
		require.NotEmpty(t, audits)
		require.Equal(t, "null", audits[len(audits)-1].Before)
		require.Contains(t, audits[len(audits)-1].After, `"status":"suspended"`)
	})
}
