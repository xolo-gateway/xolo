package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// QuotaInfoResolver computes the proto.QuotaInfo handed to pipeline plugins:
// for the current day, month and year, what remains on the most constrained
// level of the request's path. The levels are those the enforcer checks: the
// user (its effective budget, see QuotaService.ResolveEffectiveQuota), the
// organization and, for a request attributed to a team, the team and the
// member inside it. Amounts are expressed in microcents of the organization
// currency. A period without a budget at any level is reported with a zero
// total, which plugins must read as "unlimited".
type QuotaInfoResolver struct {
	quotaResolver quotaResolver
	usageStore    port.UsageStore
	now           func() time.Time
}

func NewQuotaInfoResolver(quotaResolver quotaResolver, usageStore port.UsageStore) *QuotaInfoResolver {
	return &QuotaInfoResolver{quotaResolver: quotaResolver, usageStore: usageStore, now: time.Now}
}

// quotaLevel is one budget of the request's path and the counter it is
// checked against.
type quotaLevel struct {
	scope   model.QuotaScope
	scopeID string
	budgets [3]*int64
}

// Resolve returns nil when the quota cannot be resolved or when no budget is
// configured on the request's path, so that plugins fall back to their
// neutral value instead of reacting to a spurious zero. teamID is empty for a
// request charged to the organization alone.
func (r *QuotaInfoResolver) Resolve(ctx context.Context, userID model.UserID, orgID model.OrgID, teamID model.TeamID) *proto.QuotaInfo {
	levels, err := r.levels(ctx, userID, orgID, teamID)
	if err != nil {
		slog.WarnContext(ctx, "pipeline: could not resolve effective quota", slog.Any("error", err))
		return nil
	}
	if len(levels) == 0 {
		return nil
	}

	now := r.now()
	starts := [3]time.Time{model.StartOfDay(now), model.StartOfMonth(now), model.StartOfYear(now)}
	var totals, remainings [3]float64
	for period := range starts {
		found := false
		for _, level := range levels {
			budget := level.budgets[period]
			if budget == nil {
				continue
			}
			spent, err := r.usageStore.SumQuotaCostSince(ctx, level.scope, level.scopeID, orgID, starts[period])
			if err != nil {
				slog.WarnContext(ctx, "pipeline: could not sum usage for quota", slog.Any("error", err))
				return nil
			}
			total := float64(*budget)
			remaining := max(total-float64(spent), 0)
			if !found || remaining < remainings[period] {
				totals[period], remainings[period], found = total, remaining, true
			}
		}
	}
	return &proto.QuotaInfo{
		DailyTotal: totals[0], DailyRemaining: remainings[0],
		MonthlyTotal: totals[1], MonthlyRemaining: remainings[1],
		YearlyTotal: totals[2], YearlyRemaining: remainings[2],
	}
}

// levels lists the budgets set on the request's path.
func (r *QuotaInfoResolver) levels(ctx context.Context, userID model.UserID, orgID model.OrgID, teamID model.TeamID) ([]quotaLevel, error) {
	var levels []quotaLevel
	add := func(scope model.QuotaScope, scopeID string, daily, monthly, yearly *int64) {
		if daily == nil && monthly == nil && yearly == nil {
			return
		}
		levels = append(levels, quotaLevel{scope, scopeID, [3]*int64{daily, monthly, yearly}})
	}

	effective, orgQuota, err := r.quotaResolver.ResolveEffectiveQuota(ctx, userID, orgID)
	if err != nil {
		return nil, err
	}
	add(model.QuotaScopeUser, string(userID), effective.DailyBudget, effective.MonthlyBudget, effective.YearlyBudget)
	if orgQuota != nil {
		add(model.QuotaScopeOrg, string(orgID), orgQuota.DailyBudget(), orgQuota.MonthlyBudget(), orgQuota.YearlyBudget())
	}
	if teamID == "" {
		return levels, nil
	}
	teamQuota, member, err := r.quotaResolver.ResolveTeamQuotas(ctx, teamID, userID)
	if err != nil {
		return nil, err
	}
	if teamQuota != nil {
		add(model.QuotaScopeTeam, string(teamID), teamQuota.DailyBudget(), teamQuota.MonthlyBudget(), teamQuota.YearlyBudget())
	}
	if member != nil {
		add(model.QuotaScopeTeamMember, model.TeamMemberScopeID(teamID, userID), member.DailyBudget, member.MonthlyBudget, member.YearlyBudget)
	}
	return levels, nil
}
