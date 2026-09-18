package model

import (
	"time"

	"github.com/rs/xid"
)

type QuotaID string

func NewQuotaID() QuotaID {
	return QuotaID(xid.New().String())
}

type QuotaScope string

const (
	QuotaScopeOrg         QuotaScope = "org"
	QuotaScopeUser        QuotaScope = "user"
	QuotaScopeApplication QuotaScope = "application"
)

// StartOfDay, StartOfMonth and StartOfYear return the start of the budget
// window containing t, in t's own location. Budget windows are calendar
// windows in the server's timezone, and enforcement, reporting and the running
// counters have to agree on where they begin — hence a single definition.
func StartOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func StartOfMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
}

func StartOfYear(t time.Time) time.Time {
	return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location())
}

// Quota defines a monetary budget constraint for an org or user.
// All values are in microcents (1 microcent = $0.000001). nil = unlimited.
// Currency specifies which currency the budget amounts are expressed in.
type Quota interface {
	WithID[QuotaID]

	Scope() QuotaScope
	ScopeID() string // OrgID or UserID as string
	Currency() string
	DailyBudget() *int64
	MonthlyBudget() *int64
	YearlyBudget() *int64
	CreatedAt() time.Time
	UpdatedAt() time.Time
}

type BaseQuota struct {
	id            QuotaID
	scope         QuotaScope
	scopeID       string
	currency      string
	dailyBudget   *int64
	monthlyBudget *int64
	yearlyBudget  *int64
	createdAt     time.Time
	updatedAt     time.Time
}

func (q *BaseQuota) ID() QuotaID           { return q.id }
func (q *BaseQuota) Scope() QuotaScope     { return q.scope }
func (q *BaseQuota) ScopeID() string       { return q.scopeID }
func (q *BaseQuota) Currency() string      { return q.currency }
func (q *BaseQuota) DailyBudget() *int64   { return q.dailyBudget }
func (q *BaseQuota) MonthlyBudget() *int64 { return q.monthlyBudget }
func (q *BaseQuota) YearlyBudget() *int64  { return q.yearlyBudget }
func (q *BaseQuota) CreatedAt() time.Time  { return q.createdAt }
func (q *BaseQuota) UpdatedAt() time.Time  { return q.updatedAt }

var _ Quota = &BaseQuota{}

func NewQuota(scope QuotaScope, scopeID string, currency string, daily, monthly, yearly *int64) *BaseQuota {
	if currency == "" {
		currency = DefaultCurrency
	}
	return &BaseQuota{
		id:            NewQuotaID(),
		scope:         scope,
		scopeID:       scopeID,
		currency:      currency,
		dailyBudget:   daily,
		monthlyBudget: monthly,
		yearlyBudget:  yearly,
		createdAt:     time.Now(),
		updatedAt:     time.Now(),
	}
}

// EffectiveQuota holds the resolved budget constraints after merging all levels.
// nil at each period means unlimited at that granularity.
// Currency specifies which currency the budgets are expressed in.
type EffectiveQuota struct {
	Currency      string
	DailyBudget   *int64
	MonthlyBudget *int64
	YearlyBudget  *int64
}
