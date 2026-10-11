package service_test

import (
	"context"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// teamWorld is an in-memory organization with teams, members and budgets.
type teamWorld struct {
	port.QuotaStore
	org     model.Organization
	teams   map[model.TeamID]*model.BaseTeam
	members map[model.TeamID][]model.UserID
	quotas  map[string]model.Quota
	// prefixes are the local model names starting with "<slug>/".
	prefixes map[string]bool
}

func newTeamWorld() *teamWorld {
	return &teamWorld{
		org:      model.NewOrganization("tenant", "acme", "Acme", ""),
		teams:    map[model.TeamID]*model.BaseTeam{},
		members:  map[model.TeamID][]model.UserID{},
		quotas:   map[string]model.Quota{},
		prefixes: map[string]bool{},
	}
}

func (w *teamWorld) team(slug string, members ...model.UserID) *model.BaseTeam {
	team := model.NewTeam(w.org.ID(), slug, slug, "")
	w.teams[team.ID()] = team
	w.members[team.ID()] = members
	return team
}

func (w *teamWorld) set(scope model.QuotaScope, scopeID string, cascade model.QuotaCascade, daily, monthly *int64) model.Quota {
	q := model.NewQuota(scope, scopeID, "EUR", daily, monthly, nil)
	q.SetCascade(cascade)
	w.quotas[string(scope)+"/"+scopeID] = q
	return q
}

func (w *teamWorld) GetQuota(_ context.Context, scope model.QuotaScope, scopeID string) (model.Quota, error) {
	if q, ok := w.quotas[string(scope)+"/"+scopeID]; ok {
		return q, nil
	}
	return nil, port.ErrNotFound
}

func (w *teamWorld) ListQuotas(_ context.Context, scope model.QuotaScope, scopeIDs []string) ([]model.Quota, error) {
	var out []model.Quota
	for _, id := range scopeIDs {
		if q, ok := w.quotas[string(scope)+"/"+id]; ok {
			out = append(out, q)
		}
	}
	return out, nil
}

func (w *teamWorld) GetTeamByID(_ context.Context, id model.TeamID) (model.Team, error) {
	if team, ok := w.teams[id]; ok {
		return team, nil
	}
	return nil, port.ErrNotFound
}

func (w *teamWorld) GetTeamBySlug(_ context.Context, orgID model.OrgID, slug string) (model.Team, error) {
	for _, team := range w.teams {
		if team.OrgID() == orgID && team.Slug() == slug {
			return team, nil
		}
	}
	return nil, port.ErrNotFound
}

func (w *teamWorld) ListOrgTeams(_ context.Context, orgID model.OrgID) ([]model.Team, error) {
	var out []model.Team
	for _, team := range w.teams {
		if team.OrgID() == orgID {
			out = append(out, team)
		}
	}
	return out, nil
}

func (w *teamWorld) ListTeamMembers(_ context.Context, id model.TeamID) ([]model.TeamMember, error) {
	var out []model.TeamMember
	for _, user := range w.members[id] {
		out = append(out, model.NewTeamMember(id, model.MembershipID("m-"+string(user)), user, nil))
	}
	return out, nil
}

func (w *teamWorld) ModelNamePrefixTaken(_ context.Context, _ model.OrgID, slug string) (bool, error) {
	return w.prefixes[slug], nil
}

func (w *teamWorld) GetOrgBySlug(_ context.Context, _ model.TenantID, slug string) (model.Organization, error) {
	if slug == w.org.Slug() {
		return w.org, nil
	}
	return nil, port.ErrNotFound
}

func amount(v int64) *int64 { return &v }

func requireViolation(t *testing.T, err error, mode model.QuotaCascade, period string) *service.CascadeViolation {
	t.Helper()
	var violation *service.CascadeViolation
	require.True(t, errors.As(err, &violation), "expected a cascade violation, got %v", err)
	require.ErrorIs(t, err, port.ErrInvalid)
	require.Equal(t, mode, violation.Mode)
	require.Equal(t, period, violation.Period)
	return violation
}

func TestQuotaCascadeFreeChecksNothing(t *testing.T) {
	ctx := context.Background()
	w := newTeamWorld()
	team := w.team("legal")
	w.set(model.QuotaScopeOrg, string(w.org.ID()), model.QuotaCascadeFree, nil, amount(100))

	q := model.NewQuota(model.QuotaScopeTeam, string(team.ID()), "EUR", nil, amount(500), nil)
	require.NoError(t, service.CheckQuotaCascade(ctx, w, q))
}

func TestQuotaCascadeCapped(t *testing.T) {
	ctx := context.Background()
	w := newTeamWorld()
	legal, rd := w.team("legal"), w.team("rd")
	w.set(model.QuotaScopeOrg, string(w.org.ID()), model.QuotaCascadeCapped, nil, amount(100))
	w.set(model.QuotaScopeTeam, string(rd.ID()), model.QuotaCascadeFree, nil, amount(80))

	// Overcommitting is allowed: 80 + 90 > 100, each at most 100.
	q := model.NewQuota(model.QuotaScopeTeam, string(legal.ID()), "EUR", nil, amount(90), nil)
	require.NoError(t, service.CheckQuotaCascade(ctx, w, q))

	q = model.NewQuota(model.QuotaScopeTeam, string(legal.ID()), "EUR", nil, amount(120), nil)
	v := requireViolation(t, service.CheckQuotaCascade(ctx, w, q), model.QuotaCascadeCapped, "monthly")
	require.Equal(t, int64(100), v.Limit)
	require.Equal(t, int64(120), v.Got)
	require.Equal(t, string(legal.ID()), v.ChildID)

	// A child without a budget of its own is bounded by the parent at run
	// time: it breaks nothing.
	q = model.NewQuota(model.QuotaScopeTeam, string(legal.ID()), "EUR", amount(500), nil, nil)
	require.NoError(t, service.CheckQuotaCascade(ctx, w, q), "the parent sets no daily budget")
}

func TestQuotaCascadeSplit(t *testing.T) {
	ctx := context.Background()
	w := newTeamWorld()
	legal, rd := w.team("legal"), w.team("rd")
	w.set(model.QuotaScopeOrg, string(w.org.ID()), model.QuotaCascadeSplit, nil, amount(100))
	w.set(model.QuotaScopeTeam, string(rd.ID()), model.QuotaCascadeFree, nil, amount(60))

	q := model.NewQuota(model.QuotaScopeTeam, string(legal.ID()), "EUR", nil, amount(40), nil)
	require.NoError(t, service.CheckQuotaCascade(ctx, w, q))

	q = model.NewQuota(model.QuotaScopeTeam, string(legal.ID()), "EUR", nil, amount(41), nil)
	v := requireViolation(t, service.CheckQuotaCascade(ctx, w, q), model.QuotaCascadeSplit, "monthly")
	require.Equal(t, int64(101), v.Got)

	// Saving a budget replaces its own former value in the sum.
	w.set(model.QuotaScopeTeam, string(legal.ID()), model.QuotaCascadeFree, nil, amount(40))
	q = model.NewQuota(model.QuotaScopeTeam, string(rd.ID()), "EUR", nil, amount(60), nil)
	require.NoError(t, service.CheckQuotaCascade(ctx, w, q))
}

// The cascade is checked period by period: a period one side leaves without a
// budget constrains nothing, the others still do.
func TestQuotaCascadeChecksEachPeriodOnItsOwn(t *testing.T) {
	ctx := context.Background()
	w := newTeamWorld()
	legal, rd := w.team("legal"), w.team("rd")
	w.set(model.QuotaScopeOrg, string(w.org.ID()), model.QuotaCascadeSplit, amount(10), nil)
	w.set(model.QuotaScopeTeam, string(rd.ID()), model.QuotaCascadeFree, amount(4), amount(1000))

	// The parent budgets the day only: a monthly budget takes no share of it.
	q := model.NewQuota(model.QuotaScopeTeam, string(legal.ID()), "EUR", nil, amount(500), nil)
	require.NoError(t, service.CheckQuotaCascade(ctx, w, q))

	// A daily budget over what is left is refused, whatever the month.
	q = model.NewQuota(model.QuotaScopeTeam, string(legal.ID()), "EUR", amount(7), nil, nil)
	v := requireViolation(t, service.CheckQuotaCascade(ctx, w, q), model.QuotaCascadeSplit, "daily")
	require.Equal(t, int64(11), v.Got)
}

// A parent budget is checked against its children too: lowering it below
// what is already split is refused.
func TestQuotaCascadeChecksTheChildrenOfAParent(t *testing.T) {
	ctx := context.Background()
	w := newTeamWorld()
	legal := w.team("legal", "u1", "u2")
	w.set(model.QuotaScopeTeamMember, model.TeamMemberScopeID(legal.ID(), "u1"), model.QuotaCascadeFree, amount(30), nil)
	w.set(model.QuotaScopeTeamMember, model.TeamMemberScopeID(legal.ID(), "u2"), model.QuotaCascadeFree, amount(30), nil)

	q := model.NewQuota(model.QuotaScopeTeam, string(legal.ID()), "EUR", amount(50), nil, nil)
	q.SetCascade(model.QuotaCascadeSplit)
	requireViolation(t, service.CheckQuotaCascade(ctx, w, q), model.QuotaCascadeSplit, "daily")

	q.SetCascade(model.QuotaCascadeCapped)
	require.NoError(t, service.CheckQuotaCascade(ctx, w, q))

	// A member budget is checked against its team.
	w.set(model.QuotaScopeTeam, string(legal.ID()), model.QuotaCascadeCapped, amount(50), nil)
	member := model.NewQuota(model.QuotaScopeTeamMember, model.TeamMemberScopeID(legal.ID(), "u1"), "EUR", amount(51), nil, nil)
	requireViolation(t, service.CheckQuotaCascade(ctx, w, member), model.QuotaCascadeCapped, "daily")
}

func TestCheckTeamSlug(t *testing.T) {
	ctx := context.Background()
	w := newTeamWorld()
	w.prefixes["meta"] = true

	require.NoError(t, service.CheckTeamSlug(ctx, w, w.org.ID(), "legal"))
	require.ErrorIs(t, service.CheckTeamSlug(ctx, w, w.org.ID(), "Legal"), port.ErrInvalid)
	require.ErrorIs(t, service.CheckTeamSlug(ctx, w, w.org.ID(), "meta"), port.ErrTeamNameConflict,
		"a team named meta would capture the proxy name meta/llama-3")
}

func TestCheckModelLocalName(t *testing.T) {
	ctx := context.Background()
	w := newTeamWorld()
	w.team("legal")

	require.NoError(t, service.CheckModelLocalName(ctx, w, w.org.ID(), "gpt-4o"))
	require.NoError(t, service.CheckModelLocalName(ctx, w, w.org.ID(), "meta/llama-3"))
	require.ErrorIs(t, service.CheckModelLocalName(ctx, w, w.org.ID(), "legal/gpt-4o"), port.ErrTeamNameConflict)
}

func TestCheckStaticModelRefs(t *testing.T) {
	ctx := context.Background()
	w := newTeamWorld()
	w.team("legal")
	graph := func(name string) *model.PipelineGraph {
		data := []byte(`{"proxyName":"` + name + `"}`)
		return &model.PipelineGraph{Nodes: []model.PipelineNode{{ID: "m", Type: model.NodeTypeModel, Data: data}}}
	}

	for _, ok := range []string{"gpt-4o", "acme/gpt-4o", "other/legal/gpt-4o", "meta/llama-3"} {
		require.NoError(t, service.CheckStaticModelRefs(ctx, w, w, "tenant", graph(ok)), ok)
	}
	err := service.CheckStaticModelRefs(ctx, w, w, "tenant", graph("acme/legal/gpt-4o"))
	require.ErrorIs(t, err, service.ErrTeamQualifiedReference)
	require.Contains(t, err.Error(), `"acme/gpt-4o"`, "the error suggests the name without team")
}

func TestResolveTeamQuotas(t *testing.T) {
	ctx := context.Background()
	w := newTeamWorld()
	legal := w.team("legal", "u1", "u2", "u3", "u4")
	svc := service.NewQuotaService(w, nil, service.WithQuotaTeams(w))

	team, member, err := svc.ResolveTeamQuotas(ctx, legal.ID(), "u1")
	require.NoError(t, err)
	require.Nil(t, team)
	require.Nil(t, member)

	w.set(model.QuotaScopeTeam, string(legal.ID()), model.QuotaCascadeFree, nil, amount(400))
	team, member, err = svc.ResolveTeamQuotas(ctx, legal.ID(), "u1")
	require.NoError(t, err)
	require.NotNil(t, team)
	require.Nil(t, member, "without sharing, a member has no budget of its own")

	legal.SetShareQuotaEqually(true)
	_, member, err = svc.ResolveTeamQuotas(ctx, legal.ID(), "u1")
	require.NoError(t, err)
	require.Equal(t, int64(100), *member.MonthlyBudget, "a quarter of the envelope")

	w.set(model.QuotaScopeTeamMember, model.TeamMemberScopeID(legal.ID(), "u1"), model.QuotaCascadeFree, nil, amount(10))
	_, member, err = svc.ResolveTeamQuotas(ctx, legal.ID(), "u1")
	require.NoError(t, err)
	require.Equal(t, int64(10), *member.MonthlyBudget, "a budget of its own wins over the share")

	_, member, err = svc.ResolveTeamQuotas(ctx, legal.ID(), "")
	require.NoError(t, err)
	require.Nil(t, member, "an application has no member budget")

	_, _, err = service.NewQuotaService(w, nil).ResolveTeamQuotas(ctx, legal.ID(), "u1")
	require.Error(t, err, "team budgets are never skipped silently")
}

func TestListedModelNames(t *testing.T) {
	legal := rbac.TeamAccess{TeamID: "t-legal", Slug: "legal", Grants: []rbac.ModelGrant{{ModelID: "m1", Kind: rbac.ModelKindLLM}}}
	rd := rbac.TeamAccess{TeamID: "t-rd", Slug: "rd", Grants: []rbac.ModelGrant{{ModelID: "m1", Kind: rbac.ModelKindLLM}}}

	member := rbac.NewPermissionSet(nil, []rbac.TeamAccess{legal, rd})
	require.Equal(t, []string{"acme/legal/gpt", "acme/rd/gpt"}, service.ListedModelNames(member, "", "acme", "gpt", "m1", rbac.ModelKindLLM),
		"team access is listed in the qualified form only")
	require.Empty(t, service.ListedModelNames(member, "", "acme", "other", "m2", rbac.ModelKindLLM))

	admin := rbac.NewPermissionSet([]string{string(rbac.PermModelUseOrg)}, []rbac.TeamAccess{legal})
	require.Equal(t, []string{"acme/gpt", "acme/legal/gpt"}, service.ListedModelNames(admin, "", "acme", "gpt", "m1", rbac.ModelKindLLM))
	require.Equal(t, []string{"acme/other"}, service.ListedModelNames(admin, "", "acme", "other", "m2", rbac.ModelKindLLM))

	require.Equal(t, []string{"acme/rd/gpt"}, service.ListedModelNames(member, "t-rd", "acme", "gpt", "m1", rbac.ModelKindLLM),
		"a bound token sees its team only")
	require.Equal(t, []string{"acme/legal/other"}, service.ListedModelNames(admin, "t-legal", "acme", "other", "m2", rbac.ModelKindLLM),
		"a bound token with the organization-wide access names every model through its team")
	require.Empty(t, service.ListedModelNames(member, "t-gone", "acme", "gpt", "m1", rbac.ModelKindLLM))
}
