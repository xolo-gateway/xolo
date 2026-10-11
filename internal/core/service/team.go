package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// TeamNameReader is what the naming checks read: the teams of an
// organization and the local names of its models.
type TeamNameReader interface {
	GetTeamBySlug(ctx context.Context, orgID model.OrgID, slug string) (model.Team, error)
	ModelNamePrefixTaken(ctx context.Context, orgID model.OrgID, slug string) (bool, error)
}

// CheckTeamSlug validates the slug of a new or renamed team of the
// organization; another team holding it is left to the store. A slug
// is a DNS label, and "<org>/<slug>/<rest>" must never also designate a model
// whose local name is "<slug>/<rest>".
func CheckTeamSlug(ctx context.Context, store TeamNameReader, orgID model.OrgID, slug string) error {
	if !model.IsValidSlug(slug) {
		return errors.Wrapf(port.ErrInvalid, "invalid team slug %q", slug)
	}
	taken, err := store.ModelNamePrefixTaken(ctx, orgID, slug)
	if err != nil {
		return errors.WithStack(err)
	}
	if taken {
		return errors.Wrapf(port.ErrTeamNameConflict, "a model of the organization is named %q", slug+"/…")
	}
	return nil
}

// CheckModelLocalName validates the local name of a real or virtual model of
// the organization, the proxy name or the virtual model name, against its
// team slugs: a name starting with "<team slug>/" would be read as a team
// qualified name.
func CheckModelLocalName(ctx context.Context, store TeamNameReader, orgID model.OrgID, name string) error {
	segment, _, found := strings.Cut(name, "/")
	if !found {
		return nil
	}
	_, err := store.GetTeamBySlug(ctx, orgID, segment)
	if errors.Is(err, port.ErrNotFound) {
		return nil
	}
	if err != nil {
		return errors.WithStack(err)
	}
	return errors.Wrapf(port.ErrTeamNameConflict, "%q starts with the slug of team %q", name, segment)
}

// QuotaCascadeReader is what the cascade check reads.
type QuotaCascadeReader interface {
	GetQuota(ctx context.Context, scope model.QuotaScope, scopeID string) (model.Quota, error)
	ListQuotas(ctx context.Context, scope model.QuotaScope, scopeIDs []string) ([]model.Quota, error)
	GetTeamByID(ctx context.Context, id model.TeamID) (model.Team, error)
	ListOrgTeams(ctx context.Context, orgID model.OrgID) ([]model.Team, error)
	ListTeamMembers(ctx context.Context, id model.TeamID) ([]model.TeamMember, error)
}

// ErrQuotaCascade is wrapped by every CascadeViolation.
var ErrQuotaCascade = fmt.Errorf("budget breaks the cascade of its parent: %w", port.ErrInvalid)

// CascadeViolation explains why a budget breaks a cascade.
type CascadeViolation struct {
	Mode model.QuotaCascade
	// Period is "daily", "monthly" or "yearly".
	Period string
	// Parent is the level whose cascade is broken.
	Parent model.QuotaScope
	// Child is the child level, the one exceeding the parent in capped mode.
	// In split mode, the children as a whole exceed it and Child is empty.
	Child   model.QuotaScope
	ChildID string
	// Limit is the parent budget, Got the child budget or the children total.
	Limit, Got int64
}

func (v *CascadeViolation) Error() string {
	if v.Mode == model.QuotaCascadeSplit {
		return fmt.Sprintf("the %s budgets of the %s children add up to %d, above the %s budget of %d", v.Period, v.Parent, v.Got, v.Parent, v.Limit)
	}
	return fmt.Sprintf("the %s budget of %s %s is %d, above the %s budget of %d", v.Period, v.Child, v.ChildID, v.Got, v.Parent, v.Limit)
}

func (v *CascadeViolation) Unwrap() error { return ErrQuotaCascade }

// cascadeNode is one budget of a cascade, with what identifies it.
type cascadeNode struct {
	scope   model.QuotaScope
	scopeID string
	quota   model.Quota
}

var cascadePeriods = []struct {
	name   string
	budget func(model.Quota) *int64
}{
	{"daily", model.Quota.DailyBudget},
	{"monthly", model.Quota.MonthlyBudget},
	{"yearly", model.Quota.YearlyBudget},
}

// CheckQuotaCascade checks a budget about to be saved against the cascade of
// its parent level (the organization over a team, a team over a member), and
// the cascade it sets itself against its children. It returns a
// *CascadeViolation when one is broken. Budgets are compared as stored: they
// are all in the organization currency.
func CheckQuotaCascade(ctx context.Context, store QuotaCascadeReader, quota model.Quota) error {
	parent, siblings, err := cascadeParent(ctx, store, quota)
	if err != nil {
		return err
	}
	if parent != nil && parent.Cascade() != model.QuotaCascadeFree {
		children := make([]cascadeNode, 0, len(siblings))
		for _, s := range siblings {
			if s.scopeID == quota.ScopeID() {
				continue
			}
			children = append(children, s)
		}
		children = append(children, cascadeNode{quota.Scope(), quota.ScopeID(), quota})
		if err := checkCascade(parent, children); err != nil {
			return err
		}
	}
	if !quota.Scope().HasChildren() || quota.Cascade() == model.QuotaCascadeFree {
		return nil
	}
	children, err := cascadeChildren(ctx, store, quota.Scope(), quota.ScopeID())
	if err != nil {
		return err
	}
	return checkCascade(quota, children)
}

// cascadeParent returns the budget of the parent level of quota, nil when the
// level has no parent or none is set, and the budgets of every child of that
// parent.
func cascadeParent(ctx context.Context, store QuotaCascadeReader, quota model.Quota) (model.Quota, []cascadeNode, error) {
	var (
		scope   model.QuotaScope
		scopeID string
	)
	switch quota.Scope() {
	case model.QuotaScopeTeam:
		team, err := store.GetTeamByID(ctx, model.TeamID(quota.ScopeID()))
		if err != nil {
			return nil, nil, errors.WithStack(err)
		}
		scope, scopeID = model.QuotaScopeOrg, string(team.OrgID())
	case model.QuotaScopeTeamMember:
		teamID, _, ok := model.ParseTeamMemberScopeID(quota.ScopeID())
		if !ok {
			return nil, nil, errors.Wrapf(port.ErrInvalid, "invalid team member scope %q", quota.ScopeID())
		}
		scope, scopeID = model.QuotaScopeTeam, string(teamID)
	default:
		return nil, nil, nil
	}
	parent, err := getOptionalQuota(ctx, store, scope, scopeID)
	if err != nil || parent == nil {
		return nil, nil, err
	}
	siblings, err := cascadeChildren(ctx, store, scope, scopeID)
	return parent, siblings, err
}

// cascadeChildren returns the budgets set on the children of a level: the
// teams of an organization, the members of a team.
func cascadeChildren(ctx context.Context, store QuotaCascadeReader, scope model.QuotaScope, scopeID string) ([]cascadeNode, error) {
	var (
		childScope model.QuotaScope
		ids        []string
	)
	switch scope {
	case model.QuotaScopeOrg:
		teams, err := store.ListOrgTeams(ctx, model.OrgID(scopeID))
		if err != nil {
			return nil, errors.WithStack(err)
		}
		childScope = model.QuotaScopeTeam
		for _, t := range teams {
			ids = append(ids, string(t.ID()))
		}
	case model.QuotaScopeTeam:
		members, err := store.ListTeamMembers(ctx, model.TeamID(scopeID))
		if err != nil {
			return nil, errors.WithStack(err)
		}
		childScope = model.QuotaScopeTeamMember
		for _, m := range members {
			ids = append(ids, model.TeamMemberScopeID(model.TeamID(scopeID), m.UserID()))
		}
	default:
		return nil, nil
	}
	// One read for every child, whatever the size of the organization.
	quotas, err := store.ListQuotas(ctx, childScope, ids)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	children := make([]cascadeNode, 0, len(quotas))
	for _, q := range quotas {
		children = append(children, cascadeNode{childScope, q.ScopeID(), q})
	}
	return children, nil
}

func checkCascade(parent model.Quota, children []cascadeNode) error {
	for _, period := range cascadePeriods {
		limit := period.budget(parent)
		if limit == nil {
			continue
		}
		var total int64
		for _, child := range children {
			budget := period.budget(child.quota)
			if budget == nil {
				// No budget of its own: the child is bounded by the parent at
				// run time, and takes no share of it.
				continue
			}
			if parent.Cascade() == model.QuotaCascadeCapped && *budget > *limit {
				return errors.WithStack(&CascadeViolation{
					Mode: model.QuotaCascadeCapped, Period: period.name, Parent: parent.Scope(),
					Child: child.scope, ChildID: child.scopeID, Limit: *limit, Got: *budget,
				})
			}
			total += *budget
		}
		if parent.Cascade() == model.QuotaCascadeSplit && total > *limit {
			return errors.WithStack(&CascadeViolation{
				Mode: model.QuotaCascadeSplit, Period: period.name, Parent: parent.Scope(), Limit: *limit, Got: total,
			})
		}
	}
	return nil
}

func getOptionalQuota(ctx context.Context, store QuotaCascadeReader, scope model.QuotaScope, scopeID string) (model.Quota, error) {
	q, err := store.GetQuota(ctx, scope, scopeID)
	if errors.Is(err, port.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return q, nil
}

// TeamSlugReader reads the teams of an organization by slug.
type TeamSlugReader interface {
	GetTeamBySlug(ctx context.Context, orgID model.OrgID, slug string) (model.Team, error)
}

// OrgSlugReader reads the organizations of a tenant by slug.
type OrgSlugReader interface {
	GetOrgBySlug(ctx context.Context, tenantID model.TenantID, slug string) (model.Organization, error)
}

// ErrTeamQualifiedReference signals a graph whose node configuration names a
// model qualified with a team.
var ErrTeamQualifiedReference = fmt.Errorf("a static model reference can not name a team: %w", port.ErrInvalid)

// CheckStaticModelRefs refuses a graph whose nodes name a model qualified with
// a team, "<org>/<team>/<model>". An organization graph is shared by every team
// it is granted to and the request decides which team pays; a personal graph
// charges the team its owner picked. Either way the team segment has no place
// in the configuration of a node.
func CheckStaticModelRefs(ctx context.Context, orgs OrgSlugReader, teams TeamSlugReader, tenantID model.TenantID, graph *model.PipelineGraph) error {
	for _, ref := range model.StaticModelRefs(graph) {
		var lookupErr error
		name, ok := model.ParseModelName(ref, func(orgSlug, segment string) bool {
			org, err := orgs.GetOrgBySlug(ctx, tenantID, orgSlug)
			if err != nil {
				if !errors.Is(err, port.ErrNotFound) {
					lookupErr = err
				}
				return false
			}
			_, err = teams.GetTeamBySlug(ctx, org.ID(), segment)
			if err != nil && !errors.Is(err, port.ErrNotFound) {
				lookupErr = err
			}
			return err == nil
		})
		if lookupErr != nil {
			return errors.WithStack(lookupErr)
		}
		if ok && name.Team != "" {
			return errors.Wrapf(ErrTeamQualifiedReference, "%q: use %q", ref, name.OrgLevel().String())
		}
	}
	return nil
}
