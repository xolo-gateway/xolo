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
	QuotaScopeOrg  QuotaScope = "org"
	QuotaScopeUser QuotaScope = "user"
	// QuotaScopeApplication caps the spend attributed to a single application
	// principal (an M2M token). The proxy enforces it in XoloQuotaEnforcer
	// against a running counter populated per usage record: a row that
	// references ApplicationID feeds the application counter and the org
	// counter, never the shadow user's user counter (issue #64). The
	// migration timeline that populates the counter lives next to the
	// gormigrate entry in internal/adapter/gorm/database.go.
	QuotaScopeApplication QuotaScope = "application"
	// QuotaScopeTeam caps the spend attributed to a team, every member
	// included. Its scope id is the TeamID.
	QuotaScopeTeam QuotaScope = "team"
	// QuotaScopeTeamMember caps the spend of one member inside one team. Its
	// scope id is TeamMemberScopeID(team, user).
	QuotaScopeTeamMember QuotaScope = "team_member"
)

// QuotaCascade constrains the budgets of the children of a level when they are
// saved: the teams of an organization, the members of a team. Every level on a
// request's path is checked at run time whatever the cascade; the cascade only
// keeps the configuration consistent.
type QuotaCascade string

const (
	// QuotaCascadeFree checks nothing: only the run-time limits apply.
	QuotaCascadeFree QuotaCascade = "free"
	// QuotaCascadeCapped requires each child budget to be at most the parent
	// one, period by period. The children may overcommit the parent together.
	QuotaCascadeCapped QuotaCascade = "capped"
	// QuotaCascadeSplit requires the children budgets to add up to at most the
	// parent one, period by period: the envelope is really shared.
	QuotaCascadeSplit QuotaCascade = "split"
)

// Valid reports whether c is a known cascade. The empty value is free.
func (c QuotaCascade) Valid() bool {
	switch c {
	case "", QuotaCascadeFree, QuotaCascadeCapped, QuotaCascadeSplit:
		return true
	}
	return false
}

// Normalize returns the cascade with the empty value read as free.
func (c QuotaCascade) Normalize() QuotaCascade {
	if c == "" {
		return QuotaCascadeFree
	}
	return c
}

// HasChildren reports whether budgets of the scope can constrain children
// budgets: an organization over its teams, a team over its members.
func (s QuotaScope) HasChildren() bool {
	return s == QuotaScopeOrg || s == QuotaScopeTeam
}

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
	// Cascade constrains the budgets of the children of the scope, see
	// QuotaCascade. Only meaningful on the org and team scopes.
	Cascade() QuotaCascade
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
	cascade       QuotaCascade
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
func (q *BaseQuota) Cascade() QuotaCascade { return q.cascade.Normalize() }
func (q *BaseQuota) CreatedAt() time.Time  { return q.createdAt }
func (q *BaseQuota) UpdatedAt() time.Time  { return q.updatedAt }

// SetID gives the quota an identifier chosen by the provisioning client.
func (q *BaseQuota) SetID(id QuotaID) { q.id = id }

// SetCascade sets the constraint on the children budgets of the scope.
func (q *BaseQuota) SetCascade(c QuotaCascade) { q.cascade = c.Normalize() }

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
