package gorm

import (
	"context"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateAlert implements port.AlertStore.
func (s *Store) CreateAlert(ctx context.Context, alert model.Alert) error {
	return s.recorded(ctx, tracking("alert", string(alert.ID())), func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Create(fromAlert(alert)).Error)
	})
}

// UpdateAlert implements port.AlertStore.
func (s *Store) UpdateAlert(ctx context.Context, alert model.Alert) error {
	return s.recorded(ctx, tracking("alert", string(alert.ID())), func(ctx context.Context, db *gorm.DB) error {
		result := db.Model(&Alert{}).Where("id = ?", string(alert.ID())).Save(fromAlert(alert))
		return errors.WithStack(result.Error)
	})
}

// DeleteAlert implements port.AlertStore.
func (s *Store) DeleteAlert(ctx context.Context, id model.AlertID) error {
	return s.recorded(ctx, tracking("alert", string(id)), func(ctx context.Context, db *gorm.DB) error {
		result := db.Delete(&Alert{}, "id = ?", string(id))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		return nil
	})
}

// GetAlertByID implements port.AlertStore.
func (s *Store) GetAlertByID(ctx context.Context, id model.AlertID) (model.Alert, error) {
	var a Alert
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&a, "id = ?", string(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &wrappedAlert{&a}, nil
}

// ListAlerts implements port.AlertStore.
func (s *Store) ListAlerts(ctx context.Context, orgID model.OrgID) ([]model.Alert, error) {
	var alerts []*Alert
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("org_id = ?", string(orgID)).
			Order("created_at DESC").Find(&alerts).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.Alert, 0, len(alerts))
	for _, a := range alerts {
		result = append(result, &wrappedAlert{a})
	}
	return result, nil
}

// ListEnabledAlerts implements port.AlertStore.
func (s *Store) ListEnabledAlerts(ctx context.Context) ([]model.Alert, error) {
	var alerts []*Alert
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		// The alerts of a frozen scope are not evaluated: their state can
		// not be written anymore.
		return errors.WithStack(db.Where("enabled = ?", true).Where(notFrozen("alerts", "alerts")).Find(&alerts).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.Alert, 0, len(alerts))
	for _, a := range alerts {
		result = append(result, &wrappedAlert{a})
	}
	return result, nil
}

// UpdateAlertState persists only the evaluation state of an alert, without
// touching its configuration or updated_at.
func (s *Store) UpdateAlertState(ctx context.Context, id model.AlertID, state model.AlertState, pendingSince *time.Time, lastEvaluatedAt *time.Time) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&Alert{}).Where("id = ?", string(id)).
			Updates(map[string]any{
				"state":             string(state),
				"pending_since":     pendingSince,
				"last_evaluated_at": lastEvaluatedAt,
			}).Error)
	})
}

// ClaimAlertEvaluation implements port.AlertStore. A single conditional
// UPDATE: the row lock (PostgreSQL) or the write lock (SQLite) lets one
// claimant through, the others find last_evaluated_at already moved.
func (s *Store) ClaimAlertEvaluation(ctx context.Context, id model.AlertID, now time.Time, minAge time.Duration) (bool, error) {
	// Kept to the microsecond, the precision both databases store, so the
	// transition can find it back by equality.
	now = now.Truncate(time.Microsecond)
	var claimed bool
	err := s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result := db.Model(&Alert{}).
			Where("id = ? AND (last_evaluated_at IS NULL OR last_evaluated_at <= ?)", string(id), now.Add(-minAge)).
			Update("last_evaluated_at", now)
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		claimed = result.RowsAffected == 1
		return nil
	})
	return claimed, err
}

// TransitionAlert implements port.AlertStore. One transaction holding the
// alert's row lock (PostgreSQL; SQLite runs one write transaction at a time):
// the check that the claim still holds, the incident change and the state
// write cannot interleave with another instance's. Only that alert's row is
// locked.
func (s *Store) TransitionAlert(ctx context.Context, t port.AlertTransition) (port.AlertTransitionResult, error) {
	var result port.AlertTransitionResult
	claimedAt := t.ClaimedAt.Truncate(time.Microsecond)
	err := s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result = port.AlertTransitionResult{}

		query := db.Model(&Alert{}).Select("id").
			Where("id = ? AND state = ? AND last_evaluated_at = ?", string(t.AlertID), string(t.From), claimedAt)
		if !isSQLite(db) {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var held []Alert
		if err := query.Limit(1).Find(&held).Error; err != nil {
			return errors.WithStack(err)
		}
		if len(held) == 0 {
			// Claimed again since, or moved: this decision is stale.
			return nil
		}

		var open []AlertIncident
		if err := db.Where("alert_id = ? AND resolved_at IS NULL", string(t.AlertID)).
			Order("started_at DESC").Find(&open).Error; err != nil {
			return errors.WithStack(err)
		}

		switch {
		case t.ResolveAt != nil:
			if len(open) > 0 {
				if err := db.Model(&AlertIncident{}).Where("alert_id = ? AND resolved_at IS NULL", string(t.AlertID)).
					Updates(map[string]any{"status": model.IncidentStatusResolved, "resolved_at": *t.ResolveAt}).Error; err != nil {
					return errors.WithStack(err)
				}
			}
		case len(open) > 0:
			if t.Peak != nil {
				if err := db.Model(&AlertIncident{}).Where("id = ? AND peak_value < ?", open[0].ID, *t.Peak).
					Update("peak_value", *t.Peak).Error; err != nil {
					return errors.WithStack(err)
				}
			}
		case t.Open != nil:
			if err := db.Create(fromAlertIncident(t.Open)).Error; err != nil {
				return errors.WithStack(err)
			}
			result.Opened = t.Open
		}

		if err := db.Model(&Alert{}).Where("id = ?", string(t.AlertID)).
			Updates(map[string]any{"state": string(t.To), "pending_since": t.PendingSince}).Error; err != nil {
			return errors.WithStack(err)
		}
		result.Applied = true
		return nil
	})
	return result, err
}

// ResolveIncident implements port.AlertIncidentStore.
func (s *Store) ResolveIncident(ctx context.Context, id model.AlertIncidentID, resolvedAt time.Time) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result := db.Model(&AlertIncident{}).Where("id = ?", string(id)).
			Updates(map[string]any{"status": model.IncidentStatusResolved, "resolved_at": resolvedAt})
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		return nil
	})
}

// UpdateIncidentPeak implements port.AlertIncidentStore.
func (s *Store) UpdateIncidentPeak(ctx context.Context, id model.AlertIncidentID, peak float64) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&AlertIncident{}).
			Where("id = ? AND peak_value < ?", string(id), peak).
			Update("peak_value", peak).Error)
	})
}

// GetOpenIncident implements port.AlertIncidentStore.
func (s *Store) GetOpenIncident(ctx context.Context, alertID model.AlertID) (model.AlertIncident, error) {
	var i AlertIncident
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Where("alert_id = ? AND resolved_at IS NULL", string(alertID)).
			Order("started_at DESC").First(&i).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.WithStack(port.ErrNotFound)
			}
			return errors.WithStack(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &wrappedAlertIncident{&i}, nil
}

// ListIncidents implements port.AlertIncidentStore.
func (s *Store) ListIncidents(ctx context.Context, filter port.IncidentFilter) ([]model.AlertIncident, error) {
	var incidents []*AlertIncident
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&AlertIncident{})
		if filter.OrgID != nil {
			query = query.Where("org_id = ?", string(*filter.OrgID))
		}
		if filter.AlertID != nil {
			query = query.Where("alert_id = ?", string(*filter.AlertID))
		}
		if filter.Status != nil {
			query = query.Where("status = ?", *filter.Status)
		}
		query = query.Order("started_at DESC")
		if filter.Limit != nil {
			query = query.Limit(*filter.Limit)
		}
		if filter.Offset != nil {
			query = query.Offset(*filter.Offset)
		}
		return errors.WithStack(query.Find(&incidents).Error)
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.AlertIncident, 0, len(incidents))
	for _, i := range incidents {
		result = append(result, &wrappedAlertIncident{i})
	}
	return result, nil
}

var (
	_ port.AlertStore         = &Store{}
	_ port.AlertIncidentStore = &Store{}
)
