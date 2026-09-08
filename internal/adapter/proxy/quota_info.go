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
// the effective per-user budgets (see QuotaService.ResolveEffectiveQuota) and
// what remains of them for the current day, month and year. Amounts are
// expressed in microcents of the effective quota currency. A period without a
// budget is reported with a zero total, which plugins must read as "unlimited".
type QuotaInfoResolver struct {
	quotaResolver quotaResolver
	usageStore    port.UsageStore
	now           func() time.Time
}

func NewQuotaInfoResolver(quotaResolver quotaResolver, usageStore port.UsageStore) *QuotaInfoResolver {
	return &QuotaInfoResolver{quotaResolver: quotaResolver, usageStore: usageStore, now: time.Now}
}

// Resolve returns nil when the quota cannot be resolved or when no budget is
// configured for the user, so that plugins fall back to their neutral value
// instead of reacting to a spurious zero.
func (r *QuotaInfoResolver) Resolve(ctx context.Context, userID model.UserID, orgID model.OrgID) *proto.QuotaInfo {
	effective, err := r.quotaResolver.ResolveEffectiveQuota(ctx, userID, orgID)
	if err != nil {
		slog.WarnContext(ctx, "pipeline: could not resolve effective quota", slog.Any("error", err))
		return nil
	}
	if effective.DailyBudget == nil && effective.MonthlyBudget == nil && effective.YearlyBudget == nil {
		return nil
	}

	now := r.now()
	info := &proto.QuotaInfo{}
	ok := true
	info.DailyTotal, info.DailyRemaining, ok = r.period(ctx, userID, orgID, effective.DailyBudget, startOfDay(now), ok)
	info.MonthlyTotal, info.MonthlyRemaining, ok = r.period(ctx, userID, orgID, effective.MonthlyBudget, startOfMonth(now), ok)
	info.YearlyTotal, info.YearlyRemaining, ok = r.period(ctx, userID, orgID, effective.YearlyBudget, startOfYear(now), ok)
	if !ok {
		return nil
	}
	return info
}

func (r *QuotaInfoResolver) period(ctx context.Context, userID model.UserID, orgID model.OrgID, budget *int64, since time.Time, ok bool) (total, remaining float64, stillOK bool) {
	if !ok || budget == nil {
		return 0, 0, ok
	}
	spent, err := r.usageStore.SumCostSince(ctx, userID, orgID, since)
	if err != nil {
		slog.WarnContext(ctx, "pipeline: could not sum usage for quota", slog.Any("error", err))
		return 0, 0, false
	}
	total = float64(*budget)
	remaining = total - float64(spent)
	if remaining < 0 {
		remaining = 0
	}
	return total, remaining, true
}
