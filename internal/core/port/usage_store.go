package port

import (
	"context"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type UsageStore interface {
	RecordUsage(ctx context.Context, record model.UsageRecord) error
	QueryUsage(ctx context.Context, filter UsageFilter) ([]model.UsageRecord, error)
	AggregateUsage(ctx context.Context, filter UsageFilter) (*UsageAggregate, error)
	// SumCostSince sums all PAYG (plan_covered=false) costs for a user+org since the given time.
	// It reads usage_records directly and its cost grows with the history: use it for reports,
	// never on the proxy hot path, where SumQuotaCostSince answers the same question.
	SumCostSince(ctx context.Context, userID model.UserID, orgID model.OrgID, since time.Time) (int64, error)
	// SumQuotaCostSince returns the PAYG spending of one budget scope (a user or an org,
	// always within one organization) since the given time, summed across currencies the
	// way the budget check consumes it.
	//
	// It is answered from the running per-day counters maintained when usage is recorded,
	// not by aggregating the usage history: the yearly window covers the whole table, and
	// running that scan on every proxied request made the database saturate long before
	// the gateway.
	SumQuotaCostSince(ctx context.Context, scope model.QuotaScope, scopeID string, orgID model.OrgID, since time.Time) (int64, error)
	// SumCostSinceByCurrency returns the total PAYG (plan_covered=false) cost per currency for an
	// org (and optionally a subset of users) since the given time. When userIDs is empty, all users
	// are included.
	SumCostSinceByCurrency(ctx context.Context, userIDs []model.UserID, orgID model.OrgID, since time.Time) (map[string]int64, error)
	// SumPlanUsageSince aggregates subscription-covered (plan_covered=true) usage for a specific
	// provider+org since the given time, returning total tokens and total provider-currency value
	// (in microcents of provider currency). Used to enforce rolling-window budgets.
	SumPlanUsageSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time) (tokens int64, providerValue int64, err error)
	// SumUserPlanUsageSince aggregates subscription-covered (plan_covered=true) usage for a specific
	// user+provider+org since the given time. Used to enforce per-user fair-share rolling-window budgets.
	SumUserPlanUsageSince(ctx context.Context, userID model.UserID, orgID model.OrgID, providerID model.ProviderID, since time.Time) (tokens int64, providerValue int64, err error)
	// CountActivePlanUsersSince counts the distinct users who consumed
	// subscription-covered (plan_covered=true) usage for a provider+org since the
	// given time. It sizes the shared part of the per-user fair-share allocation,
	// so that the plan budget left unused by quiet members is redistributed to the
	// members actually competing for it. Requests without a user (application
	// tokens) are not counted.
	//
	// excludeUserID, when set, is left out of the count. Callers allocating for a
	// user add them back unconditionally, which is exact whether or not they have
	// consumed yet — inferring their presence from a zero usage sum is not, since
	// a request can be recorded with no billable token.
	CountActivePlanUsersSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time, excludeUserID model.UserID) (int64, error)
	// HasPlanUsageSince reports whether the user consumed subscription-covered
	// (plan_covered=true) usage for a provider+org since the given time. It is a
	// probe that stops at the first matching row, bounded to the caller's rows on
	// the window by idx_usage_org_prov_user. The allocator uses it to tell whether
	// the caller is already among the counted active users, so the expensive
	// count itself can be shared by everyone on the plan.
	HasPlanUsageSince(ctx context.Context, userID model.UserID, orgID model.OrgID, providerID model.ProviderID, since time.Time) (bool, error)
	// EarliestPlanUsageSince returns the creation time of the oldest subscription-covered
	// (plan_covered=true) usage record for a provider+org still inside the rolling window
	// starting at `since`. Used to display when the window will next free up. Returns a zero
	// time when no such record exists.
	EarliestPlanUsageSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time) (time.Time, error)
	// AggregateCostByDimension returns PAYG (plan_covered=false) cost sub-totals grouped by
	// the given dimension, the record's org and currency, honoring the filter. It lets the
	// usage/dashboard charts bucket costs in SQL instead of loading every record into memory.
	// The org and currency are returned so callers can convert each sub-total to a display
	// currency exactly as a per-record loop would.
	AggregateCostByDimension(ctx context.Context, filter UsageFilter, dimension UsageDimension) ([]DimensionCost, error)
	// AggregatePlanTokensByUser returns subscription-covered (plan_covered=true) token
	// sub-totals grouped by user, honoring the filter.
	AggregatePlanTokensByUser(ctx context.Context, filter UsageFilter) ([]UserTokenUsage, error)
}

// UsageDimension identifies a grouping axis for AggregateCostByDimension.
type UsageDimension string

const (
	// UsageDimensionDay groups by calendar day (server-local) of the record.
	UsageDimensionDay UsageDimension = "day"
	// UsageDimensionModel groups by effective model name (resolved name when set,
	// otherwise the proxy model name).
	UsageDimensionModel UsageDimension = "model"
	// UsageDimensionUser groups by user id.
	UsageDimensionUser UsageDimension = "user"
	// UsageDimensionProvider groups by provider id.
	UsageDimensionProvider UsageDimension = "provider"
)

// DimensionCost is a PAYG cost sub-total for one value of a grouping dimension,
// scoped to a single org and currency. Callers convert Cost from Currency to the
// display currency of OrgID.
type DimensionCost struct {
	Key      string
	OrgID    model.OrgID
	Currency string
	Cost     int64
}

// UserTokenUsage is a token count aggregated for a single user.
type UserTokenUsage struct {
	UserID model.UserID
	Tokens int64
}

type UsageFilter struct {
	UserID         *model.UserID
	UserIDs        []model.UserID
	ApplicationID  *model.ApplicationID
	ApplicationIDs []model.ApplicationID
	OrgID          *model.OrgID
	ModelID        *model.LLMModelID
	ProviderID     *model.ProviderID
	AuthTokenID    *string
	Currency       *string
	ProxyModelName *string
	PlanCovered    *bool
	Since          *time.Time
	Until          *time.Time
	Limit          *int
	Offset         *int
}

type UsageAggregate struct {
	TotalRequests    int64
	TotalCost        int64  // microcents, in org's currency
	Currency         string // org's currency
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}
