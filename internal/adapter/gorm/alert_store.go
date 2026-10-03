package gorm

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// CreateAlert implements port.AlertStore.
func (s *Store) createAlert(ctx context.Context, alert model.Alert) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Create(fromAlert(alert)).Error)
	})
}

// UpdateAlert implements port.AlertStore.
func (s *Store) updateAlert(ctx context.Context, alert model.Alert) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result := db.Model(&Alert{}).Where("id = ?", string(alert.ID())).Save(fromAlert(alert))
		return errors.WithStack(result.Error)
	})
}

// DeleteAlert implements port.AlertStore.
func (s *Store) deleteAlert(ctx context.Context, id model.AlertID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
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
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
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
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
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
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("enabled = ?", true).Find(&alerts).Error)
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

// UpdateAlertState implements port.AlertStore.
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

// CreateIncident implements port.AlertIncidentStore.
func (s *Store) CreateIncident(ctx context.Context, incident model.AlertIncident) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Create(fromAlertIncident(incident)).Error)
	})
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
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
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
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
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

func (s *Store) CreateAlert(ctx context.Context, alert model.Alert) error {
	return s.mutate(ctx, "alert", string(alert.ID()), func(bound *Store) error { return bound.createAlert(ctx, alert) })
}

func (s *Store) UpdateAlert(ctx context.Context, alert model.Alert) error {
	return s.mutate(ctx, "alert", string(alert.ID()), func(bound *Store) error { return bound.updateAlert(ctx, alert) })
}

func (s *Store) DeleteAlert(ctx context.Context, id model.AlertID) error {
	return s.mutate(ctx, "alert", string(id), func(bound *Store) error { return bound.deleteAlert(ctx, id) })
}
