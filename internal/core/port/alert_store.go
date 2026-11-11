package port

import (
	"context"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type AlertStore interface {
	CreateAlert(ctx context.Context, alert model.Alert) error
	UpdateAlert(ctx context.Context, alert model.Alert) error
	DeleteAlert(ctx context.Context, id model.AlertID) error
	GetAlertByID(ctx context.Context, id model.AlertID) (model.Alert, error)
	ListAlerts(ctx context.Context, orgID model.OrgID) ([]model.Alert, error)
	// ListEnabledAlerts returns every enabled alert across all orgs, for the
	// periodic evaluator.
	ListEnabledAlerts(ctx context.Context) ([]model.Alert, error)
	// TransitionAlert applies the outcome of one evaluation at once: the new
	// state and its effect on the open incident. It applies nothing, and
	// reports so, unless the alert is still in state From with the
	// last_evaluated_at its claim set (ClaimedAt): an evaluation that outlasted
	// its claim, overtaken by another instance's, must not touch the incident
	// that other decision acted on. Best-effort on the refusing side, like the
	// claim: replayed after a commit the client could not confirm, it reports
	// that it applied nothing.
	TransitionAlert(ctx context.Context, transition AlertTransition) (AlertTransitionResult, error)
	// ClaimAlertEvaluation reserves the evaluation of an alert at now for the
	// caller: it moves last_evaluated_at to now when the alert was never
	// evaluated or not within minAge, atomically, and reports whether it did.
	// Every instance sharing the database runs the evaluator; the claim makes
	// one of them evaluate each alert per period, so two cannot each open an
	// incident for the same crossing. It is best-effort on the refusing side:
	// a claim whose transaction had to be retried after it committed reports
	// false, which skips one period and never doubles one. It compares
	// timestamps written by
	// different hosts: their clocks must agree well within minAge, which NTP
	// gives by orders of magnitude.
	ClaimAlertEvaluation(ctx context.Context, id model.AlertID, now time.Time, minAge time.Duration) (bool, error)
}

type AlertIncidentStore interface {
	ResolveIncident(ctx context.Context, id model.AlertIncidentID, resolvedAt time.Time) error
	UpdateIncidentPeak(ctx context.Context, id model.AlertIncidentID, peak float64) error
	// GetOpenIncident returns the currently firing (unresolved) incident for an
	// alert, or ErrNotFound when none is open.
	GetOpenIncident(ctx context.Context, alertID model.AlertID) (model.AlertIncident, error)
	ListIncidents(ctx context.Context, filter IncidentFilter) ([]model.AlertIncident, error)
}

type IncidentFilter struct {
	OrgID   *model.OrgID
	AlertID *model.AlertID
	Status  *string
	Limit   *int
	Offset  *int
}

// AlertTransition is the outcome of one evaluation of an alert.
type AlertTransition struct {
	AlertID model.AlertID
	// ClaimedAt is the last_evaluated_at the evaluation's claim wrote.
	ClaimedAt time.Time
	From      model.AlertState
	To        model.AlertState
	// PendingSince is the new start of the dwell period, nil outside it.
	PendingSince *time.Time
	// Open is opened unless an incident is open already.
	Open model.AlertIncident
	// Peak raises the peak value of the open incident.
	Peak *float64
	// ResolveAt resolves the open incident, if any.
	ResolveAt *time.Time
}

// AlertTransitionResult tells what TransitionAlert did.
type AlertTransitionResult struct {
	Applied bool
	// Opened is the incident it opened, nil when it opened none.
	Opened model.AlertIncident
}
