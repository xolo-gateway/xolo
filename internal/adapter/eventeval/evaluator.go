// Package eventeval implements the periodic alert evaluator: it evaluates each
// enabled alert's threshold rule over the events in its rolling window and drives
// the ok → pending → firing → resolved state machine, opening incidents and
// pinning the contributing events.
package eventeval

import (
	"context"
	"log/slog"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/eventql"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
)

// evaluationScanCap bounds how many events are fetched per alert evaluation.
const evaluationScanCap = 10000

type Evaluator struct {
	alertStore    port.AlertStore
	incidentStore port.AlertIncidentStore
	eventStore    port.EventStore
	interval      time.Duration
}

func NewEvaluator(alertStore port.AlertStore, incidentStore port.AlertIncidentStore, eventStore port.EventStore, interval time.Duration) *Evaluator {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Evaluator{
		alertStore:    alertStore,
		incidentStore: incidentStore,
		eventStore:    eventStore,
		interval:      interval,
	}
}

// Run evaluates alerts on a ticker until ctx is cancelled.
func (e *Evaluator) Run(ctx context.Context) error {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			e.evaluateAll(ctx)
		}
	}
}

// EvaluateAllForTest runs a single evaluation pass synchronously. Exported for
// tests only.
func (e *Evaluator) EvaluateAllForTest(ctx context.Context) {
	e.evaluateAll(ctx)
}

func (e *Evaluator) evaluateAll(ctx context.Context) {
	alerts, err := e.alertStore.ListEnabledAlerts(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "alert evaluator: could not list enabled alerts", slog.Any("error", errors.WithStack(err)))
		return
	}
	for _, alert := range alerts {
		// Every instance runs this loop: only the one that claims the alert
		// evaluates it, the others would act on the state it is changing. Half
		// the interval lets the next pass through even when the instances'
		// tickers drift, and keeps a second instance from evaluating again
		// right after the first.
		now := time.Now().Truncate(time.Microsecond)
		// Evaluated too recently for anyone to claim it: spare the write.
		if last := alert.LastEvaluatedAt(); last != nil && now.Sub(*last) < e.interval/2 {
			continue
		}
		claimed, err := e.alertStore.ClaimAlertEvaluation(ctx, alert.ID(), now, e.interval/2)
		if err != nil {
			slog.ErrorContext(ctx, "alert evaluator: could not claim the evaluation",
				slog.String("alertID", string(alert.ID())),
				slog.Any("error", errors.WithStack(err)))
			continue
		}
		if !claimed {
			continue
		}
		// Decide on the state as it is now, not as listed at the start of the
		// pass: another instance may have evaluated it in between.
		alertID := alert.ID()
		reloaded, err := e.alertStore.GetAlertByID(ctx, alertID)
		if err != nil {
			// Deleted since the listing, typically: nothing to evaluate.
			if !errors.Is(err, port.ErrNotFound) {
				slog.ErrorContext(ctx, "alert evaluator: could not reload the claimed alert",
					slog.String("alertID", string(alertID)),
					slog.Any("error", errors.WithStack(err)))
			}
			continue
		}
		alert = reloaded
		if err := e.evaluateAlert(ctx, alert, now); err != nil {
			slog.ErrorContext(ctx, "alert evaluator: evaluation failed",
				slog.String("alertID", string(alert.ID())),
				slog.Any("error", errors.WithStack(err)))
		}
	}
}

// evaluateAlert decides the outcome of the evaluation claimed at claimedAt and
// applies it through TransitionAlert, which drops it when another instance
// claimed the alert since: a stale decision then has no effect at all, on the
// state or on the incidents.
func (e *Evaluator) evaluateAlert(ctx context.Context, alert model.Alert, claimedAt time.Time) error {
	compiled, err := eventql.Compile(alert.Query())
	if err != nil {
		return errors.Wrapf(err, "invalid query for alert %s", alert.ID())
	}

	since := time.Now().Add(-alert.Window())
	orgID := alert.OrgID()
	limit := evaluationScanCap
	filter := port.EventFilter{
		OrgID: &orgID,
		Since: &since,
		Query: compiled,
		Limit: &limit,
	}
	// Personal alerts only evaluate their owner's own events; org alerts span all
	// users of the organization.
	if alert.Scope() == model.AlertScopePersonal {
		owner := alert.OwnerID()
		filter.UserID = &owner
	} else {
		filter.AllUsers = true
	}
	events, err := e.eventStore.QueryEvents(ctx, filter)
	if err != nil {
		return errors.WithStack(err)
	}

	value := float64(len(events))
	conditionMet := model.CompareThreshold(alert.Comparator(), value, alert.Threshold())

	now := time.Now()
	transition := port.AlertTransition{
		AlertID:   alert.ID(),
		ClaimedAt: claimedAt,
		From:      alert.State(),
		To:        alert.State(),
	}

	if conditionMet {
		switch alert.State() {
		case model.AlertStateFiring:
			// Still firing: raise the incident peak, and open one should none
			// be open, so a firing alert always has its incident.
			transition.Peak = &value
			transition.Open = model.NewAlertIncident(alert.ID(), alert.OrgID(), value)
		default:
			// ok or pending: start/continue the dwell period.
			pendingSince := alert.PendingSince()
			if alert.State() != model.AlertStatePending || pendingSince == nil {
				pendingSince = &now
			}
			if now.Sub(*pendingSince) >= alert.For() {
				transition.To = model.AlertStateFiring
				transition.Open = model.NewAlertIncident(alert.ID(), alert.OrgID(), value)
			} else {
				transition.To = model.AlertStatePending
				transition.PendingSince = pendingSince
			}
		}
	} else {
		// Whatever the recorded state, an incident still open is closed.
		transition.To = model.AlertStateOK
		transition.ResolveAt = &now
	}

	result, err := e.alertStore.TransitionAlert(ctx, transition)
	if err != nil {
		return errors.WithStack(err)
	}
	if result.Opened != nil {
		e.pin(ctx, alert, result.Opened, events, value)
	}
	return nil
}

// pin attaches the contributing events to the incident just opened.
func (e *Evaluator) pin(ctx context.Context, alert model.Alert, incident model.AlertIncident, events []model.Event, value float64) {
	ids := make([]model.EventID, 0, len(events))
	for _, ev := range events {
		ids = append(ids, ev.ID())
	}
	if err := e.eventStore.PinEvents(ctx, ids, incident.ID()); err != nil {
		slog.ErrorContext(ctx, "alert evaluator: could not pin incident events", slog.Any("error", errors.WithStack(err)))
	}

	slog.InfoContext(ctx, "alert firing",
		slog.String("alertID", string(alert.ID())),
		slog.String("incidentID", string(incident.ID())),
		slog.Float64("value", value))
}
