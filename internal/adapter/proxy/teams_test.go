package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/crypto"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	authn "github.com/xolo-gateway/xolo/internal/http/middleware/authn"
	"github.com/xolo-gateway/xolo/internal/pipeline"
	"github.com/xolo-gateway/xolo/internal/pipeline/pipelinetest"
)

const teamsTestSecretKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

// teamsWorld is an organization "acme" with two real models, gpt and mistral,
// and the teams, virtual models and personal virtual models a test adds.
type teamsWorld struct {
	org      model.Organization
	provider model.Provider
	models   map[string]model.LLMModel
	teams    map[string]model.Team
	vms      *pipelinetest.VirtualModelStore
	pvms     map[string]model.PersonalVirtualModel
	user     model.User
}

func newTeamsWorld(t *testing.T) *teamsWorld {
	t.Helper()
	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	key, err := crypto.Encrypt(teamsTestSecretKey, "sk-test")
	require.NoError(t, err)
	provider := model.NewProvider(org.ID(), "openai", "openai", "http://127.0.0.1:1", key, "USD")
	w := &teamsWorld{
		org:      org,
		provider: provider,
		models:   map[string]model.LLMModel{},
		teams:    map[string]model.Team{},
		vms:      pipelinetest.NewVirtualModelStore(),
		pvms:     map[string]model.PersonalVirtualModel{},
		user:     model.NewUser(testTenantID, "oidc", "alice", "alice@acme.test", "Alice", true),
	}
	for _, name := range []string{"gpt", "mistral"} {
		w.models[name] = model.NewLLMModel(provider.ID(), org.ID(), name, name+"-real", "", 1000, 1000)
	}
	return w
}

func (w *teamsWorld) addTeam(slug string, granted ...string) model.Team {
	team := model.NewTeam(w.org.ID(), slug, slug, "")
	var grants []model.ModelGrant
	for _, name := range granted {
		if m, ok := w.models[name]; ok {
			grants = append(grants, model.ModelGrant{ModelID: string(m.ID()), Kind: rbac.ModelKindLLM})
			continue
		}
		vm, err := w.vms.GetVirtualModelByName(context.Background(), w.org.ID(), name)
		if err != nil {
			panic("unknown model " + name)
		}
		grants = append(grants, model.ModelGrant{ModelID: string(vm.ID()), Kind: rbac.ModelKindVirtual})
	}
	team.SetModelGrants(grants)
	w.teams[slug] = team
	return team
}

func (w *teamsWorld) access(team model.Team) rbac.TeamAccess {
	ta := rbac.TeamAccess{TeamID: string(team.ID()), Slug: team.Slug()}
	for _, g := range team.ModelGrants() {
		ta.Grants = append(ta.Grants, rbac.ModelGrant{ModelID: g.ModelID, Kind: g.Kind})
	}
	return ta
}

// ctx authenticates the world's user with the given organization permissions
// and teams, bound to a team when bound is set.
func (w *teamsWorld) ctx(codes []string, teams []model.Team, bound model.Team) context.Context {
	var access []rbac.TeamAccess
	for _, team := range teams {
		access = append(access, w.access(team))
	}
	perms := rbac.NewPermissionSet(codes, access)
	ctx := httpCtx.SetUser(context.Background(), w.user)
	ctx = httpCtx.SetTenant(ctx, model.NewTenant("default", "Default", ""))
	ctx = httpCtx.SetPermissionResolver(ctx, func(context.Context, model.OrgID) (rbac.PermissionSet, error) { return perms, nil })
	authnUser := &authn.User{OrgID: string(w.org.ID()), TokenID: "token-1"}
	if bound != nil {
		authnUser.TeamID = string(bound.ID())
	}
	return authn.SetContextUser(ctx, authnUser)
}

func (w *teamsWorld) request(name string) *genaiProxy.ProxyRequest {
	return &genaiProxy.ProxyRequest{
		Type:     genaiProxy.RequestTypeChatCompletion,
		Model:    name,
		UserID:   string(w.user.ID()),
		Body:     []byte(`{"model":"` + name + `","messages":[{"role":"user","content":"hi"}]}`),
		Metadata: map[string]any{MetaOrgID: string(w.org.ID())},
	}
}

func (w *teamsWorld) hook() *TeamScopeHook {
	return NewTeamScopeHook(teamsOrgStore{w: w}, teamsTeamStore{w}, teamsProviderStore{w: w}, w.vms, teamsPVMStore{w: w})
}

func (w *teamsWorld) adapter() *PipelineHookAdapter {
	router := NewOrgModelRouter(teamsProviderStore{w: w}, teamsOrgStore{w: w}, teamsTestSecretKey)
	return NewPipelineHookAdapter(pipelinetest.NewPluginProvider(), w.vms, teamsPVMStore{w: w}, teamsProviderStore{w: w}, teamsOrgStore{w: w}, nil, router)
}

// The fakes embed their port, nil: a method the tests should not reach
// panics.
type teamsOrgStore struct {
	port.OrgStore
	w *teamsWorld
}

func (s teamsOrgStore) GetOrgByID(_ context.Context, id model.OrgID) (model.Organization, error) {
	if id == s.w.org.ID() {
		return s.w.org, nil
	}
	return nil, port.ErrNotFound
}

func (s teamsOrgStore) GetOrgBySlug(_ context.Context, _ model.TenantID, slug string) (model.Organization, error) {
	if slug == s.w.org.Slug() {
		return s.w.org, nil
	}
	return nil, port.ErrNotFound
}

type teamsTeamStore struct{ w *teamsWorld }

func (s teamsTeamStore) GetTeamByID(_ context.Context, id model.TeamID) (model.Team, error) {
	for _, team := range s.w.teams {
		if team.ID() == id {
			return team, nil
		}
	}
	return nil, port.ErrNotFound
}

func (s teamsTeamStore) GetTeamBySlug(_ context.Context, orgID model.OrgID, slug string) (model.Team, error) {
	if team, ok := s.w.teams[slug]; ok && team.OrgID() == orgID {
		return team, nil
	}
	return nil, port.ErrNotFound
}

type teamsProviderStore struct {
	port.ProviderStore
	w *teamsWorld
}

func (s teamsProviderStore) GetLLMModelByProxyName(_ context.Context, orgID model.OrgID, name string) (model.LLMModel, error) {
	if m, ok := s.w.models[name]; ok && orgID == s.w.org.ID() {
		return m, nil
	}
	return nil, port.ErrNotFound
}

func (s teamsProviderStore) GetLLMModelByID(_ context.Context, id model.LLMModelID) (model.LLMModel, error) {
	for _, m := range s.w.models {
		if m.ID() == id {
			return m, nil
		}
	}
	return nil, port.ErrNotFound
}

func (s teamsProviderStore) GetProviderByID(context.Context, model.ProviderID) (model.Provider, error) {
	return s.w.provider, nil
}

func (s teamsProviderStore) ListEnabledLLMModels(context.Context, model.OrgID) ([]model.LLMModel, error) {
	var out []model.LLMModel
	for _, m := range s.w.models {
		out = append(out, m)
	}
	return out, nil
}

type teamsPVMStore struct {
	port.PersonalVirtualModelStore
	w *teamsWorld
}

func (s teamsPVMStore) GetPersonalVirtualModelByName(_ context.Context, userID model.UserID, name string) (model.PersonalVirtualModel, error) {
	if pvm, ok := s.w.pvms[name]; ok && pvm.UserID() == userID {
		return pvm, nil
	}
	return nil, port.ErrNotFound
}

func (s teamsPVMStore) ListPersonalVirtualModels(context.Context, model.UserID) ([]model.PersonalVirtualModel, error) {
	var out []model.PersonalVirtualModel
	for _, pvm := range s.w.pvms {
		out = append(out, pvm)
	}
	return out, nil
}

func errorCode(t *testing.T, result *genaiProxy.HookResult) string {
	t.Helper()
	require.NotNil(t, result, "expected the request to be refused")
	body, _ := result.Response.Body.(map[string]any)
	payload, _ := body["error"].(map[string]any)
	code, _ := payload["code"].(string)
	return code
}

func TestTeamScopeAttributesQualifiedNames(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	req := w.request("acme/legal/gpt")

	result, err := w.hook().PreRequest(w.ctx(nil, []model.Team{legal}, nil), req)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, string(legal.ID()), string(TeamIDFromMeta(req.Metadata)))
	require.Equal(t, "legal", TeamSlugFromMeta(req.Metadata))
	require.Equal(t, "acme/gpt", req.Model, "later hooks see the name without its team segment")
	require.Equal(t, "acme/legal/gpt", RequestedModelFromMeta(req.Metadata))
}

// A name whose first segment is a team rather than the organization is not a
// name of the organization: it reaches the router as sent, charged to no team.
func TestTeamScopeLeavesANameWithoutItsOrganization(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	req := w.request("legal/gpt")

	result, err := w.hook().PreRequest(w.ctx(nil, []model.Team{legal}, nil), req)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, "legal/gpt", req.Model)
	require.Empty(t, TeamIDFromMeta(req.Metadata))
}

func TestTeamScopeRefusesATeamOfOthers(t *testing.T) {
	w := newTeamsWorld(t)
	w.addTeam("legal", "gpt")
	rd := w.addTeam("rd", "mistral")

	ctx := w.ctx(nil, []model.Team{rd}, nil)
	requireAnsweredLikeAnUnknownTeam(t, w, ctx, "acme/legal/gpt", "acme/nosuch/gpt")
}

// requireAnsweredLikeAnUnknownTeam checks that naming a team the caller can
// not charge gets the very answer of a team that does not exist: nothing lets
// a member list the teams of the organization.
func requireAnsweredLikeAnUnknownTeam(t *testing.T, w *teamsWorld, ctx context.Context, foreign, unknown string) {
	t.Helper()
	answer := func(name string) string {
		req := w.request(name)
		result, err := w.hook().PreRequest(ctx, req)
		require.NoError(t, err)
		require.Nil(t, result, "the router answers")
		require.Empty(t, TeamIDFromMeta(req.Metadata), "no team is attributed")
		router := NewOrgModelRouter(teamsProviderStore{w: w}, teamsOrgStore{w: w}, teamsTestSecretKey)
		_, _, err = router.ResolveModel(ctx, req)
		require.Error(t, err)
		return strings.ReplaceAll(err.Error(), name, "<name>")
	}
	require.Equal(t, answer(unknown), answer(foreign))
}

func TestTeamScopeRefusesAModelTheTeamDoesNotGrant(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")

	result, err := w.hook().PreRequest(w.ctx(nil, []model.Team{legal}, nil), w.request("acme/legal/mistral"))
	require.NoError(t, err)
	require.Equal(t, "model_forbidden", errorCode(t, result))
}

func TestTeamScopeShortNameWithASinglePath(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	rd := w.addTeam("rd", "mistral")
	req := w.request("acme/gpt")

	result, err := w.hook().PreRequest(w.ctx(nil, []model.Team{legal, rd}, nil), req)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, string(legal.ID()), string(TeamIDFromMeta(req.Metadata)), "the only team granting the model is charged")
}

func TestTeamScopeShortNameReachableThroughSeveralTeams(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	rd := w.addTeam("rd", "gpt")

	result, err := w.hook().PreRequest(w.ctx(nil, []model.Team{legal, rd}, nil), w.request("acme/gpt"))
	require.NoError(t, err)
	require.Equal(t, "model_ambiguous", errorCode(t, result))
	require.Equal(t, http.StatusBadRequest, result.Response.StatusCode)
	body := result.Response.Body.(map[string]any)["error"].(map[string]any)
	require.Equal(t, []string{"acme/legal/gpt", "acme/rd/gpt"}, body["candidates"])
}

func TestTeamScopeOrgWideAccessChargesTheOrganization(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	req := w.request("acme/gpt")

	result, err := w.hook().PreRequest(w.ctx([]string{string(rbac.PermModelUseOrg)}, []model.Team{legal}, nil), req)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Empty(t, TeamIDFromMeta(req.Metadata))
}

func TestTeamScopeBoundToken(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	rd := w.addTeam("rd", "gpt")
	ctx := w.ctx(nil, []model.Team{legal, rd}, legal)

	req := w.request("acme/gpt")
	result, err := w.hook().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Nil(t, result, "a bound token is never ambiguous")
	require.Equal(t, string(legal.ID()), string(TeamIDFromMeta(req.Metadata)))

	// A bound token can not name another team.
	requireAnsweredLikeAnUnknownTeam(t, w, ctx, "acme/rd/gpt", "acme/nosuch/gpt")
}

// A bound token charges its team even for a model the team does not grant,
// when its holder reaches that model through model:use:org.
func TestTeamScopeBoundTokenWithOrgWideAccess(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	ctx := w.ctx([]string{string(rbac.PermModelUseOrg)}, []model.Team{legal}, legal)

	req := w.request("acme/mistral")
	result, err := w.hook().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, string(legal.ID()), string(TeamIDFromMeta(req.Metadata)))
}

func TestTeamScopeTokenBoundToATeamLeft(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")

	result, err := w.hook().PreRequest(w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, legal), w.request("acme/gpt"))
	require.NoError(t, err)
	require.Equal(t, "model_forbidden", errorCode(t, result))
}

// A token bound to a team of another organization charges nothing here, even
// with the organization-wide access: the permissions only hold the teams of
// the organization the request is made in.
func TestTeamScopeTokenBoundToAnotherOrganizationsTeam(t *testing.T) {
	w := newTeamsWorld(t)
	rd := w.addTeam("rd", "gpt")
	other := model.NewOrganization(testTenantID, "globex", "Globex", "")
	foreign := model.NewTeam(other.ID(), "legal", "legal", "")
	w.teams["globex-legal"] = foreign
	ctx := w.ctx([]string{string(rbac.PermModelUseOrg)}, []model.Team{rd}, foreign)

	for _, name := range []string{"acme/gpt", "acme/rd/gpt"} {
		result, err := w.hook().PreRequest(ctx, w.request(name))
		require.NoError(t, err)
		require.Equal(t, "model_forbidden", errorCode(t, result), name)
	}
}

// A personal virtual model delegates nothing: before teams, it only checked
// personal-vm:create, then resolved any model of the organization, whatever
// the model grants of its author (issue #141).
func TestPersonalVirtualModelCannotReachUngrantedModels(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	pvm := model.NewPersonalVirtualModel(w.user.ID(), "sneaky", "")
	pvm.SetTeamID(legal.ID())
	pvm.SetGraph(pipelinetest.NewGraph().Generator("in").ModelWithProxy("m", "mistral").Sink("out").
		Edge("in", "request", "m", "request").Edge("m", "response", "out", "response").Build())
	w.pvms["sneaky"] = pvm
	ctx := w.ctx([]string{string(rbac.PermPersonalVMCreate)}, []model.Team{legal}, nil)

	req := w.request("~/sneaky")
	result, err := w.hook().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, string(legal.ID()), string(TeamIDFromMeta(req.Metadata)), "the team the personal model charges")

	result, err = w.adapter().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "model_forbidden", errorCode(t, result))
}

func TestPersonalVirtualModelReachesItsTeamModels(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	pvm := model.NewPersonalVirtualModel(w.user.ID(), "mine", "")
	pvm.SetTeamID(legal.ID())
	pvm.SetGraph(pipelinetest.NewGraph().Generator("in").ModelWithProxy("m", "gpt").Sink("out").
		Edge("in", "request", "m", "request").Edge("m", "response", "out", "response").Build())
	w.pvms["mine"] = pvm
	ctx := w.ctx([]string{string(rbac.PermPersonalVMCreate)}, []model.Team{legal}, nil)

	req := w.request("~/mine")
	result, err := w.hook().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Nil(t, result)
	result, err = w.adapter().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, string(w.models["gpt"].ID()), string(ModelIDFromMeta(req.Metadata)))
}

// An organization virtual model delegates its static references: a team
// granted the virtual model uses what its author configured.
func TestOrgVirtualModelDelegatesItsStaticReferences(t *testing.T) {
	w := newTeamsWorld(t)
	vm := model.NewVirtualModel(w.org.ID(), "assistant", "")
	vm.SetGraph(pipelinetest.NewGraph().Generator("in").ModelWithProxy("m", "mistral").Sink("out").
		Edge("in", "request", "m", "request").Edge("m", "response", "out", "response").Build())
	w.vms.Add(vm)
	support := w.addTeam("support", "assistant")
	ctx := w.ctx(nil, []model.Team{support}, nil)

	req := w.request("acme/support/assistant")
	result, err := w.hook().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Nil(t, result)
	result, err = w.adapter().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Nil(t, result, "mistral is a static reference of the granted virtual model")
	require.Equal(t, string(w.models["mistral"].ID()), string(ModelIDFromMeta(req.Metadata)))
}

// A name reaching a model node at run time is not delegated: it is checked
// against the access of the request.
func TestOrgVirtualModelChecksDynamicNames(t *testing.T) {
	w := newTeamsWorld(t)
	vm := model.NewVirtualModel(w.org.ID(), "router", "")
	vm.SetGraph(pipelinetest.NewGraph().Generator("in").Value("name", "string", "mistral").Model("m").Sink("out").
		Edge("in", "request", "m", "request").Edge("name", "value", "m", "model_name").
		Edge("m", "response", "out", "response").Build())
	w.vms.Add(vm)
	support := w.addTeam("support", "router")
	ctx := w.ctx(nil, []model.Team{support}, nil)

	req := w.request("acme/support/router")
	result, err := w.hook().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Nil(t, result)
	result, err = w.adapter().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "model_forbidden", errorCode(t, result))
}

// teamQuotaResolver sets budgets on the team levels only.
type teamQuotaResolver struct {
	appQuotaResolver
	team   model.Quota
	member *model.EffectiveQuota
	calls  []model.UserID
}

func (r *teamQuotaResolver) ResolveTeamQuotas(_ context.Context, _ model.TeamID, userID model.UserID) (model.Quota, *model.EffectiveQuota, error) {
	r.calls = append(r.calls, userID)
	return r.team, r.member, nil
}

func teamQuotaRequest(t *testing.T, w *teamsWorld, team model.Team) *genaiProxy.ProxyRequest {
	t.Helper()
	req := w.request("acme/gpt")
	req.Metadata[MetaModelID] = string(w.models["gpt"].ID())
	req.Metadata[MetaTeamID] = string(team.ID())
	return req
}

func TestQuotaEnforcerChecksTheTeamLevels(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	monthly := int64(1_000)
	resolver := &teamQuotaResolver{
		appQuotaResolver: appQuotaResolver{userQuota: &model.EffectiveQuota{}, appQuota: &model.EffectiveQuota{}},
		team:             &fakeQuota{scope: model.QuotaScopeTeam, currency: "EUR", monthly: &monthly},
	}
	usage := &endToEndScopedUsage{byScope: map[model.QuotaScope]int64{model.QuotaScopeTeam: 1_000}}
	enforcer := NewXoloQuotaEnforcer(resolver, usage, teamsProviderStore{w: w})

	result, err := enforcer.PreRequest(context.Background(), teamQuotaRequest(t, w, legal))
	require.NoError(t, err)
	require.NotNil(t, result, "the team envelope is spent")
	require.Equal(t, http.StatusTooManyRequests, result.Response.StatusCode)
	require.Contains(t, errorMessage(result), "Team monthly budget exceeded")

	usage.byScope[model.QuotaScopeTeam] = 10
	resolver.member = &model.EffectiveQuota{Currency: "EUR", MonthlyBudget: &monthly}
	usage.byScope[model.QuotaScopeTeamMember] = 2_000
	result, err = enforcer.PreRequest(context.Background(), teamQuotaRequest(t, w, legal))
	require.NoError(t, err)
	require.Contains(t, errorMessage(result), "Team member monthly budget exceeded")

	usage.byScope[model.QuotaScopeTeamMember] = 10
	result, err = enforcer.PreRequest(context.Background(), teamQuotaRequest(t, w, legal))
	require.NoError(t, err)
	require.Nil(t, result)

	// A request charged to the organization alone does not read the team
	// budgets.
	calls := len(resolver.calls)
	req := teamQuotaRequest(t, w, legal)
	delete(req.Metadata, MetaTeamID)
	result, err = enforcer.PreRequest(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Len(t, resolver.calls, calls)

	// An application bound to the team has no member budget.
	req = teamQuotaRequest(t, w, legal)
	req.Metadata[MetaApplicationID] = "app-1"
	_, err = enforcer.PreRequest(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, model.UserID(""), resolver.calls[len(resolver.calls)-1])

	// But it spends the team envelope like a member does.
	usage.byScope[model.QuotaScopeTeam] = 1_000
	req = teamQuotaRequest(t, w, legal)
	req.Metadata[MetaApplicationID] = "app-1"
	result, err = enforcer.PreRequest(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result, "the team envelope is spent for its applications too")
	require.Equal(t, http.StatusTooManyRequests, result.Response.StatusCode)
	require.Contains(t, errorMessage(result), "Team monthly budget exceeded")
}

func errorMessage(result *genaiProxy.HookResult) string {
	body, _ := result.Response.Body.(map[string]any)
	payload, _ := body["error"].(map[string]any)
	message, _ := payload["message"].(string)
	return message
}

type teamsMiddlewareStore struct {
	port.MiddlewareStore
	mws []model.Middleware
}

func (s teamsMiddlewareStore) ListEnabledMiddlewares(context.Context, model.OrgID) ([]model.Middleware, error) {
	return s.mws, nil
}

type teamsTeamLister struct{ teams []model.Team }

func (l teamsTeamLister) ListOrgTeams(context.Context, model.OrgID) ([]model.Team, error) {
	return l.teams, nil
}

// Organization middlewares run first, then those of the team the request is
// attributed to; the middlewares of other teams do not run.
func TestMiddlewaresRunByLevel(t *testing.T) {
	w := newTeamsWorld(t)
	middleware := func(name string, priority int) *model.BaseMiddleware {
		mw := model.NewMiddleware(w.org.ID(), name, "")
		mw.SetEnabled(true)
		mw.SetPriority(priority)
		mw.SetAppliesToAll(true)
		mw.SetGraph(pipelinetest.NewGraph().Generator("in").ModelPassthrough("m").Sink("out").
			Edge("in", "request", "m", "request").Edge("m", "response", "out", "response").Build())
		return mw
	}
	compliance := middleware("compliance", 10)
	legalAudit := middleware("legal-audit", 1)
	rdCache := middleware("rd-cache", 0)
	legal := w.addTeam("legal", "gpt")
	legal.(*model.BaseTeam).SetMiddlewareIDs([]model.MiddlewareID{legalAudit.ID()})
	rd := w.addTeam("rd", "gpt")
	rd.(*model.BaseTeam).SetMiddlewareIDs([]model.MiddlewareID{rdCache.ID()})

	adapter := w.adapter().WithTeams(teamsTeamLister{teams: []model.Team{legal, rd}})
	ordered, err := adapter.orderedMiddlewares(context.Background(), w.org.ID(), legal.ID(),
		[]model.Middleware{rdCache, legalAudit, compliance})
	require.NoError(t, err)
	var names []string
	for _, mw := range ordered {
		names = append(names, mw.Name())
	}
	require.Equal(t, []string{"compliance", "legal-audit"}, names)

	ordered, err = adapter.orderedMiddlewares(context.Background(), w.org.ID(), "", []model.Middleware{rdCache, legalAudit, compliance})
	require.NoError(t, err)
	require.Len(t, ordered, 1, "a request charged to the organization runs the organization middlewares only")
	require.Equal(t, "compliance", ordered[0].Name())
}

func TestCompleterResolvesQualifiedNames(t *testing.T) {
	w := newTeamsWorld(t)
	ctx := w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, nil)
	_, realModel, err := w.adapter().modelExecutor.ResolveClient(ctx, "acme/gpt", pipelineEC(w))
	require.NoError(t, err)
	require.Equal(t, "gpt-real", realModel)
}

// failingOrgStore fails every organization lookup by slug.
type failingOrgStore struct {
	teamsOrgStore
}

func (failingOrgStore) GetOrgBySlug(context.Context, model.TenantID, string) (model.Organization, error) {
	return nil, errors.New("store unavailable")
}

// A store failure on the organization of a qualified name is reported, never
// read as a local name of the caller's organization.
func TestResolveRealModelReportsOrganizationLookupErrors(t *testing.T) {
	w := newTeamsWorld(t)
	router := NewOrgModelRouter(teamsProviderStore{w: w}, failingOrgStore{teamsOrgStore{w: w}}, teamsTestSecretKey)
	ctx := w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, nil)

	_, _, _, err := router.ResolveRealModel(ctx, w.org.ID(), "other/gpt")
	require.ErrorContains(t, err, "store unavailable")

	_, realModel, _, err := router.ResolveRealModel(ctx, w.org.ID(), "gpt")
	require.NoError(t, err, "a local name needs no organization lookup")
	require.Equal(t, "gpt-real", realModel)
}

// A failing team lookup fails the request: reading the segment as part of the
// model name would answer it as naming an unknown model.
func TestTeamScopeReportsTeamLookupErrors(t *testing.T) {
	w := newTeamsWorld(t)
	w.addTeam("legal", "gpt")
	hook := NewTeamScopeHook(teamsOrgStore{w: w}, failingTeamStore{teamsTeamStore{w}}, teamsProviderStore{w: w}, w.vms, teamsPVMStore{w: w})

	_, err := hook.PreRequest(w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, nil), w.request("acme/legal/gpt"))
	require.ErrorContains(t, err, "store unavailable")
}

// A failing model lookup fails the access check: read as an unknown model, it
// would be let through to a resolution that grants nothing of its own.
func TestModelLookupErrorsFailClosed(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	ctx := w.ctx(nil, []model.Team{legal}, nil)

	access := newRequestModelAccess(w.org, string(legal.ID()), "legal", teamsOrgStore{w: w}, failingProviderStore{teamsProviderStore{w: w}}, w.vms)
	_, err := access.CanUse(ctx, "mistral")
	require.ErrorContains(t, err, "store unavailable")

	hook := NewTeamScopeHook(teamsOrgStore{w: w}, teamsTeamStore{w}, failingProviderStore{teamsProviderStore{w: w}}, w.vms, teamsPVMStore{w: w})
	_, err = hook.PreRequest(ctx, w.request("acme/legal/gpt"))
	require.ErrorContains(t, err, "store unavailable")
}

// A request naming an unknown model of a team is refused by the router: no
// team is recorded for it.
func TestTeamScopeUnknownModelRecordsNoTeam(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	req := w.request("acme/legal/nosuch")

	result, err := w.hook().PreRequest(w.ctx(nil, []model.Team{legal}, nil), req)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, "acme/nosuch", req.Model)
	require.Empty(t, TeamIDFromMeta(req.Metadata))
}

type failingProviderStore struct{ teamsProviderStore }

func (failingProviderStore) GetLLMModelByProxyName(context.Context, model.OrgID, string) (model.LLMModel, error) {
	return nil, errors.New("store unavailable")
}

type failingTeamStore struct{ teamsTeamStore }

func (failingTeamStore) GetTeamBySlug(context.Context, model.OrgID, string) (model.Team, error) {
	return nil, errors.New("store unavailable")
}

func pipelineEC(w *teamsWorld) pipeline.ExecutionContext {
	return pipeline.ExecutionContext{OrgID: string(w.org.ID()), UserID: string(w.user.ID()), VisitedVMs: map[model.VirtualModelID]struct{}{}}
}

// A first segment naming no organization is dropped by the resolution: the
// access check reads the name the same way.
func TestAccessReadsUnknownOrganizationsLikeTheResolution(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	access := newRequestModelAccess(w.org, string(legal.ID()), "legal", teamsOrgStore{w: w}, teamsProviderStore{w: w}, w.vms)
	ctx := w.ctx(nil, []model.Team{legal}, nil)

	ok, err := access.CanUse(ctx, "bogus/mistral")
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = access.CanUse(ctx, "bogus/gpt")
	require.NoError(t, err)
	require.True(t, ok)
}

func personalModel(w *teamsWorld, name string, team model.TeamID) {
	pvm := model.NewPersonalVirtualModel(w.user.ID(), name, "")
	pvm.SetTeamID(team)
	pvm.SetGraph(pipelinetest.NewGraph().Generator("in").ModelWithProxy("m", "gpt").Sink("out").
		Edge("in", "request", "m", "request").Edge("m", "response", "out", "response").Build())
	w.pvms[name] = pvm
}

// The team a personal virtual model charges is never swapped for another
// payer: once its author left it, the request is refused until the author
// picks another team, even with the organization-wide access.
func TestPersonalVirtualModelWhoseTeamWasLeft(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	rd := w.addTeam("rd", "gpt")
	personalModel(w, "mine", legal.ID())
	codes := []string{string(rbac.PermPersonalVMCreate), string(rbac.PermModelUseOrg)}

	result, err := w.hook().PreRequest(w.ctx(codes, []model.Team{rd}, nil), w.request("~/mine"))
	require.NoError(t, err)
	require.Equal(t, "model_team_required", errorCode(t, result))
	require.Equal(t, http.StatusBadRequest, result.Response.StatusCode)

	// A deleted team is refused the same way.
	delete(w.teams, "legal")
	result, err = w.hook().PreRequest(w.ctx(codes, []model.Team{rd}, nil), w.request("~/mine"))
	require.NoError(t, err)
	require.Equal(t, "model_team_required", errorCode(t, result))

	// A token bound to a team charges it whatever the model's own team.
	req := w.request("~/mine")
	result, err = w.hook().PreRequest(w.ctx(codes, []model.Team{rd}, rd), req)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, string(rd.ID()), string(TeamIDFromMeta(req.Metadata)))
}

// A personal virtual model charging a team of another organization is refused,
// rather than charged to a team of this one.
func TestPersonalVirtualModelOfAnotherOrganizationsTeam(t *testing.T) {
	w := newTeamsWorld(t)
	rd := w.addTeam("rd", "gpt")
	other := model.NewOrganization(testTenantID, "globex", "Globex", "")
	foreign := model.NewTeam(other.ID(), "legal", "legal", "")
	w.teams["globex-legal"] = foreign
	personalModel(w, "mine", foreign.ID())
	codes := []string{string(rbac.PermPersonalVMCreate), string(rbac.PermModelUseOrg)}

	result, err := w.hook().PreRequest(w.ctx(codes, []model.Team{rd}, nil), w.request("~/mine"))
	require.NoError(t, err)
	require.Equal(t, "model_team_required", errorCode(t, result))
	require.Equal(t, http.StatusBadRequest, result.Response.StatusCode)
}

// failingPVMStore fails every personal virtual model lookup.
type failingPVMStore struct{ teamsPVMStore }

func (failingPVMStore) GetPersonalVirtualModelByName(context.Context, model.UserID, string) (model.PersonalVirtualModel, error) {
	return nil, errors.New("store unavailable")
}

// A failing personal virtual model lookup fails the request: read as no model,
// it would charge the single team left instead of the one its owner picked.
func TestPersonalVirtualModelLookupErrorsFailTheRequest(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	rd := w.addTeam("rd", "gpt")
	personalModel(w, "mine", legal.ID())
	hook := NewTeamScopeHook(teamsOrgStore{w: w}, teamsTeamStore{w}, teamsProviderStore{w: w}, w.vms, failingPVMStore{teamsPVMStore{w: w}})

	req := w.request("~/mine")
	_, err := hook.PreRequest(w.ctx([]string{string(rbac.PermPersonalVMCreate)}, []model.Team{rd}, nil), req)
	require.ErrorContains(t, err, "store unavailable")
	require.Empty(t, TeamIDFromMeta(req.Metadata))
}

// A personal virtual model without team is refused when several paths exist.
func TestPersonalVirtualModelWithoutTeam(t *testing.T) {
	w := newTeamsWorld(t)
	legal := w.addTeam("legal", "gpt")
	rd := w.addTeam("rd", "gpt")
	personalModel(w, "mine", "")
	ctx := w.ctx([]string{string(rbac.PermPersonalVMCreate)}, []model.Team{legal, rd}, nil)

	result, err := w.hook().PreRequest(ctx, w.request("~/mine"))
	require.NoError(t, err)
	require.Equal(t, "model_team_required", errorCode(t, result))

	req := w.request("~/mine")
	result, err = w.hook().PreRequest(w.ctx([]string{string(rbac.PermPersonalVMCreate)}, []model.Team{legal}, nil), req)
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, string(legal.ID()), string(TeamIDFromMeta(req.Metadata)), "the single team is charged")
}
