package model

import (
	"time"

	"github.com/rs/xid"
)

type AlertID string

func NewAlertID() AlertID {
	return AlertID(xid.New().String())
}

// AlertState is the current state of an alert rule following a Prometheus-like
// state machine: ok → pending → firing → (ok when the condition clears).
type AlertState string

const (
	AlertStateOK      AlertState = "ok"
	AlertStatePending AlertState = "pending"
	AlertStateFiring  AlertState = "firing"
)

// AlertComparator compares the aggregated value against the threshold.
type AlertComparator string

const (
	ComparatorGT  AlertComparator = ">"
	ComparatorGTE AlertComparator = ">="
	ComparatorLT  AlertComparator = "<"
	ComparatorLTE AlertComparator = "<="
	ComparatorEQ  AlertComparator = "=="
)

// AlertAggregation is the aggregation applied over the matched events within the
// window. Only count is supported in V1.
type AlertAggregation string

const (
	AggregationCount AlertAggregation = "count"
)

// AlertScope determines which events an alert evaluates.
//   - AlertScopeOrg: all events of the organization (requires events:write).
//   - AlertScopePersonal: only the owner's own events (requires events:alerts:own).
type AlertScope string

const (
	AlertScopeOrg      AlertScope = "org"
	AlertScopePersonal AlertScope = "personal"
)

// CompareThreshold reports whether value satisfies "value <cmp> threshold".
func CompareThreshold(cmp AlertComparator, value, threshold float64) bool {
	switch cmp {
	case ComparatorGT:
		return value > threshold
	case ComparatorGTE:
		return value >= threshold
	case ComparatorLT:
		return value < threshold
	case ComparatorLTE:
		return value <= threshold
	case ComparatorEQ:
		return value == threshold
	}
	return false
}

// Alert is a threshold rule evaluated periodically over the events matching its
// eventql query within a rolling window. When the aggregated value crosses the
// threshold (and stays there for `For`), an AlertIncident is opened.
type Alert interface {
	WithID[AlertID]

	OrgID() OrgID
	OwnerID() UserID
	Scope() AlertScope
	Name() string
	Description() string
	Query() string
	Aggregation() AlertAggregation
	Window() time.Duration
	Comparator() AlertComparator
	Threshold() float64
	// For is the dwell duration the condition must hold before pending → firing.
	For() time.Duration
	Enabled() bool

	State() AlertState
	PendingSince() *time.Time
	LastEvaluatedAt() *time.Time

	CreatedAt() time.Time
	UpdatedAt() time.Time
}

type BaseAlert struct {
	id              AlertID
	orgID           OrgID
	ownerID         UserID
	scope           AlertScope
	name            string
	description     string
	query           string
	aggregation     AlertAggregation
	window          time.Duration
	comparator      AlertComparator
	threshold       float64
	forDuration     time.Duration
	enabled         bool
	state           AlertState
	pendingSince    *time.Time
	lastEvaluatedAt *time.Time
	createdAt       time.Time
	updatedAt       time.Time
}

func (a *BaseAlert) ID() AlertID                   { return a.id }
func (a *BaseAlert) OrgID() OrgID                  { return a.orgID }
func (a *BaseAlert) OwnerID() UserID               { return a.ownerID }
func (a *BaseAlert) Scope() AlertScope {
	if a.scope == "" {
		return AlertScopeOrg
	}
	return a.scope
}
func (a *BaseAlert) Name() string                 { return a.name }
func (a *BaseAlert) Description() string           { return a.description }
func (a *BaseAlert) Query() string                { return a.query }
func (a *BaseAlert) Aggregation() AlertAggregation { return a.aggregation }
func (a *BaseAlert) Window() time.Duration         { return a.window }
func (a *BaseAlert) Comparator() AlertComparator   { return a.comparator }
func (a *BaseAlert) Threshold() float64            { return a.threshold }
func (a *BaseAlert) For() time.Duration            { return a.forDuration }
func (a *BaseAlert) Enabled() bool                { return a.enabled }
func (a *BaseAlert) State() AlertState            { return a.state }
func (a *BaseAlert) PendingSince() *time.Time      { return a.pendingSince }
func (a *BaseAlert) LastEvaluatedAt() *time.Time   { return a.lastEvaluatedAt }
func (a *BaseAlert) CreatedAt() time.Time         { return a.createdAt }
func (a *BaseAlert) UpdatedAt() time.Time         { return a.updatedAt }

// SetID gives the alert an identifier chosen by the provisioning client.
func (a *BaseAlert) SetID(id AlertID) { a.id = id }

func (a *BaseAlert) SetState(s AlertState)          { a.state = s }
func (a *BaseAlert) SetPendingSince(t *time.Time)   { a.pendingSince = t }
func (a *BaseAlert) SetLastEvaluatedAt(t *time.Time) { a.lastEvaluatedAt = t }

var _ Alert = &BaseAlert{}

type AlertOption func(*BaseAlert)

func WithAlertDescription(desc string) AlertOption {
	return func(a *BaseAlert) { a.description = desc }
}
func WithAlertQuery(query string) AlertOption { return func(a *BaseAlert) { a.query = query } }
func WithAlertAggregation(agg AlertAggregation) AlertOption {
	return func(a *BaseAlert) { a.aggregation = agg }
}
func WithAlertWindow(w time.Duration) AlertOption { return func(a *BaseAlert) { a.window = w } }
func WithAlertComparator(c AlertComparator) AlertOption {
	return func(a *BaseAlert) { a.comparator = c }
}
func WithAlertThreshold(t float64) AlertOption { return func(a *BaseAlert) { a.threshold = t } }
func WithAlertFor(d time.Duration) AlertOption { return func(a *BaseAlert) { a.forDuration = d } }
func WithAlertEnabled(v bool) AlertOption      { return func(a *BaseAlert) { a.enabled = v } }
func WithAlertName(name string) AlertOption    { return func(a *BaseAlert) { a.name = name } }
func WithAlertScope(scope AlertScope) AlertOption {
	return func(a *BaseAlert) { a.scope = scope }
}

// NewAlert creates a new alert rule with sensible defaults (count aggregation,
// greater-than comparator, enabled, state ok).
func NewAlert(orgID OrgID, ownerID UserID, name string, opts ...AlertOption) *BaseAlert {
	a := &BaseAlert{
		id:          NewAlertID(),
		orgID:       orgID,
		ownerID:     ownerID,
		scope:       AlertScopeOrg,
		name:        name,
		aggregation: AggregationCount,
		comparator:  ComparatorGT,
		window:      5 * time.Minute,
		enabled:     true,
		state:       AlertStateOK,
		createdAt:   time.Now(),
		updatedAt:   time.Now(),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// UpdateAlert returns a copy of the given alert with the options applied and the
// updatedAt timestamp refreshed.
func UpdateAlert(alert Alert, opts ...AlertOption) *BaseAlert {
	a := &BaseAlert{
		id:              alert.ID(),
		orgID:           alert.OrgID(),
		ownerID:         alert.OwnerID(),
		scope:           alert.Scope(),
		name:            alert.Name(),
		description:     alert.Description(),
		query:           alert.Query(),
		aggregation:     alert.Aggregation(),
		window:          alert.Window(),
		comparator:      alert.Comparator(),
		threshold:       alert.Threshold(),
		forDuration:     alert.For(),
		enabled:         alert.Enabled(),
		state:           alert.State(),
		pendingSince:    alert.PendingSince(),
		lastEvaluatedAt: alert.LastEvaluatedAt(),
		createdAt:       alert.CreatedAt(),
		updatedAt:       time.Now(),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}
