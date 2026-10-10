package service

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// OrgProvider is a narrow interface used by QuotaService.
// It is satisfied by port.OrgStore (and any fake with just these two methods).
type OrgProvider interface {
	GetOrgByID(ctx context.Context, id model.OrgID) (model.Organization, error)
	ListOrgMembers(ctx context.Context, orgID model.OrgID, opts port.ListOrgMembersOptions) ([]model.Membership, int64, error)
}

// TeamProvider is a narrow interface used by QuotaService to resolve team
// budgets. It is satisfied by port.TeamStore.
type TeamProvider interface {
	GetTeamByID(ctx context.Context, id model.TeamID) (model.Team, error)
	ListTeamMembers(ctx context.Context, id model.TeamID) ([]model.TeamMember, error)
}

type QuotaService struct {
	quotaStore port.QuotaStore
	orgStore   OrgProvider
	teamStore  TeamProvider
}

type QuotaServiceOption func(*QuotaService)

// WithQuotaTeams lets the service resolve team budgets. Without it, a request
// attributed to a team fails rather than skipping the team budgets.
func WithQuotaTeams(teams TeamProvider) QuotaServiceOption {
	return func(s *QuotaService) { s.teamStore = teams }
}

func NewQuotaService(quotaStore port.QuotaStore, orgStore OrgProvider, opts ...QuotaServiceOption) *QuotaService {
	s := &QuotaService{quotaStore: quotaStore, orgStore: orgStore}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ResolveTeamQuotas returns the budgets of the team levels of a request
// attributed to teamID: the team budget itself, nil when none is set, and the
// member budget of userID inside the team. The member budget is the member's
// own one when set; otherwise, when the team shares its budget equally, the
// member's share of it; otherwise nil. An empty userID (an application bound
// to the team) has no member budget.
//
// Each level is checked against its own counter: these budgets are not merged
// with the user and organization ones, which keep their own checks.
func (s *QuotaService) ResolveTeamQuotas(ctx context.Context, teamID model.TeamID, userID model.UserID) (model.Quota, *model.EffectiveQuota, error) {
	if s.teamStore == nil {
		return nil, nil, errors.New("quota service: team budgets requested without a team store")
	}
	teamQuota, err := s.optionalQuota(ctx, model.QuotaScopeTeam, string(teamID))
	if err != nil {
		return nil, nil, err
	}
	if userID == "" {
		return teamQuota, nil, nil
	}
	memberQuota, err := s.optionalQuota(ctx, model.QuotaScopeTeamMember, model.TeamMemberScopeID(teamID, userID))
	if err != nil {
		return nil, nil, err
	}
	if memberQuota != nil {
		return teamQuota, &model.EffectiveQuota{
			Currency:      memberQuota.Currency(),
			DailyBudget:   memberQuota.DailyBudget(),
			MonthlyBudget: memberQuota.MonthlyBudget(),
			YearlyBudget:  memberQuota.YearlyBudget(),
		}, nil
	}
	if teamQuota == nil {
		return nil, nil, nil
	}
	team, err := s.teamStore.GetTeamByID(ctx, teamID)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	if !team.ShareQuotaEqually() {
		return teamQuota, nil, nil
	}
	members, err := s.teamStore.ListTeamMembers(ctx, teamID)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	n := int64(len(members))
	if n == 0 {
		// A request charged to a team without members (a member removed since
		// its token was minted) has no share: the undivided envelope bounds it.
		return teamQuota, nil, nil
	}
	return teamQuota, &model.EffectiveQuota{
		Currency:      teamQuota.Currency(),
		DailyBudget:   divPtr(teamQuota.DailyBudget(), n),
		MonthlyBudget: divPtr(teamQuota.MonthlyBudget(), n),
		YearlyBudget:  divPtr(teamQuota.YearlyBudget(), n),
	}, nil
}

func (s *QuotaService) optionalQuota(ctx context.Context, scope model.QuotaScope, scopeID string) (model.Quota, error) {
	q, err := s.quotaStore.GetQuota(ctx, scope, scopeID)
	if errors.Is(err, port.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return q, nil
}

func (s *QuotaService) ResolveEffectiveQuota(
	ctx context.Context,
	userID model.UserID,
	orgID model.OrgID,
) (*model.EffectiveQuota, model.Quota, error) {
	userQuota, err := s.quotaStore.GetQuota(ctx, model.QuotaScopeUser, string(userID))
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		return nil, nil, errors.WithStack(err)
	}
	if errors.Is(err, port.ErrNotFound) {
		userQuota = nil
	}

	orgQuota, err := s.quotaStore.GetQuota(ctx, model.QuotaScopeOrg, string(orgID))
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		return nil, nil, errors.WithStack(err)
	}
	if errors.Is(err, port.ErrNotFound) {
		orgQuota = nil
	}

	org, err := s.orgStore.GetOrgByID(ctx, orgID)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}

	effective := &model.EffectiveQuota{}

	// A quota record with all-nil budgets is treated as "no personal quota".
	userHasPersonalQuota := userQuota != nil &&
		(userQuota.DailyBudget() != nil || userQuota.MonthlyBudget() != nil || userQuota.YearlyBudget() != nil)

	// Currency: org quota > user quota > default.
	switch {
	case orgQuota != nil:
		effective.Currency = orgQuota.Currency()
	case userHasPersonalQuota:
		effective.Currency = userQuota.Currency()
	default:
		effective.Currency = model.DefaultCurrency
	}

	// Sharing: distribute org quota equally only when user has no personal quota.
	if !userHasPersonalQuota && org.ShareQuotaEqually() && orgQuota != nil {
		members, _, err := s.orgStore.ListOrgMembers(ctx, orgID, port.ListOrgMembersOptions{})
		if err != nil {
			return nil, nil, errors.WithStack(err)
		}
		n := int64(len(members))
		if n > 0 {
			// Synthesise a per-user quota by integer division (floor truncation).
			effective.DailyBudget = divPtr(orgQuota.DailyBudget(), n)
			effective.MonthlyBudget = divPtr(orgQuota.MonthlyBudget(), n)
			effective.YearlyBudget = divPtr(orgQuota.YearlyBudget(), n)
			// The synthesized per-user budget is only the member's share; the
			// global org cap must remain enforceable, so the raw org quota is
			// still returned for the enforcer's org-wide block (issue #82).
			return effective, orgQuota, nil
		}
		// n == 0: unlimited (theoretically impossible if requester is a member).
		return effective, orgQuota, nil
	}

	// Default: min-merge of user and org quotas.
	effective.DailyBudget = minPtrSvc(ptrOf(userQuota, func(q model.Quota) *int64 { return q.DailyBudget() }),
		ptrOf(orgQuota, func(q model.Quota) *int64 { return q.DailyBudget() }))
	effective.MonthlyBudget = minPtrSvc(ptrOf(userQuota, func(q model.Quota) *int64 { return q.MonthlyBudget() }),
		ptrOf(orgQuota, func(q model.Quota) *int64 { return q.MonthlyBudget() }))
	effective.YearlyBudget = minPtrSvc(ptrOf(userQuota, func(q model.Quota) *int64 { return q.YearlyBudget() }),
		ptrOf(orgQuota, func(q model.Quota) *int64 { return q.YearlyBudget() }))

	// Return the raw org quota as the second value so the budget enforcer can
	// reuse the lookup we just did instead of hitting the store again on the
	// hot path. nil carries the same meaning as a GetQuota that returned
	// port.ErrNotFound (issue #82).
	return effective, orgQuota, nil
}

func divPtr(v *int64, n int64) *int64 {
	if v == nil {
		return nil
	}
	r := *v / n
	return &r
}

// ResolveEffectiveQuotaForApplication merges the application and org quotas at
// each period, taking the minimum of the two non-nil values. Currency is taken
// from the org quota first so the same defaults ResolveEffectiveQuota uses
// apply here.
//
// There is no "sharing" branch on this path: an application has no membership
// to share an org budget across, and an operator who set a budget on the
// application means the whole of it, not a slice.
//
// The result is fed to the enforcer's application-scope check, which runs
// against the QuotaScopeApplication counter. The shadow user's user-scope
// check is not run at all on the application path: the enforcer skips it
// (see XoloQuotaEnforcer.PreRequest) because the counter is never fed for an
// application record (see quotaUsageRows). The application budget is the only
// budget the enforcer ever checks for a request carrying an ApplicationID
// (issue #64).
func (s *QuotaService) ResolveEffectiveQuotaForApplication(
	ctx context.Context,
	appID model.ApplicationID,
	orgID model.OrgID,
) (*model.EffectiveQuota, model.Quota, error) {
	appQuota, err := s.quotaStore.GetQuota(ctx, model.QuotaScopeApplication, string(appID))
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		return nil, nil, errors.WithStack(err)
	}
	if errors.Is(err, port.ErrNotFound) {
		appQuota = nil
	}

	orgQuota, err := s.quotaStore.GetQuota(ctx, model.QuotaScopeOrg, string(orgID))
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		return nil, nil, errors.WithStack(err)
	}
	if errors.Is(err, port.ErrNotFound) {
		orgQuota = nil
	}

	effective := &model.EffectiveQuota{}
	// Currency precedence: org quota first, then app quota, then default.
	// Both quota writers freeze budget amounts in the org's currency at
	// SetQuota time (the handler does the conversion), so even if an operator
	// somehow wrote a QuotaScopeApplication row in a different currency, its
	// microcent amounts are still in org currency by the time we min-merge
	// them. Picking the org currency here keeps the merged EffectiveQuota
	// internally consistent: every *int64 it returns is denominated in the
	// same currency.
	switch {
	case orgQuota != nil:
		effective.Currency = orgQuota.Currency()
	case appQuota != nil:
		effective.Currency = appQuota.Currency()
	default:
		effective.Currency = model.DefaultCurrency
	}

	effective.DailyBudget = minPtrSvc(
		ptrOf(appQuota, func(q model.Quota) *int64 { return q.DailyBudget() }),
		ptrOf(orgQuota, func(q model.Quota) *int64 { return q.DailyBudget() }),
	)
	effective.MonthlyBudget = minPtrSvc(
		ptrOf(appQuota, func(q model.Quota) *int64 { return q.MonthlyBudget() }),
		ptrOf(orgQuota, func(q model.Quota) *int64 { return q.MonthlyBudget() }),
	)
	effective.YearlyBudget = minPtrSvc(
		ptrOf(appQuota, func(q model.Quota) *int64 { return q.YearlyBudget() }),
		ptrOf(orgQuota, func(q model.Quota) *int64 { return q.YearlyBudget() }),
	)

	// Return the raw org quota as the second value so the budget enforcer can
	// reuse the lookup we just did instead of hitting the store again on the
	// hot path. nil carries the same meaning as a GetQuota that returned
	// port.ErrNotFound. Without it the enforcer would silently turn the org
	// cap off for application traffic the day a future resolver forgets the
	// field — see the regression Bornholm flagged on issue #82.
	return effective, orgQuota, nil
}

func ptrOf(q model.Quota, f func(model.Quota) *int64) *int64 {
	if q == nil {
		return nil
	}
	return f(q)
}

func minPtrSvc(a, b *int64) *int64 {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if *a < *b {
		return a
	}
	return b
}
