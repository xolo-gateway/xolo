package gorm

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateEnrollmentRule implements port.EnrollmentStore.
func (s *Store) CreateEnrollmentRule(ctx context.Context, rule model.EnrollmentRule) error {
	return s.recorded(ctx, tracking("enrollment_rule", string(rule.ID())), func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Create(fromEnrollmentRule(rule)).Error)
	})
}

// UpdateEnrollmentRule implements port.EnrollmentStore.
func (s *Store) UpdateEnrollmentRule(ctx context.Context, rule model.EnrollmentRule) error {
	return s.recorded(ctx, tracking("enrollment_rule", string(rule.ID())), func(ctx context.Context, db *gorm.DB) error {
		result := db.Model(&EnrollmentRule{}).Where("id = ?", string(rule.ID())).Save(fromEnrollmentRule(rule))
		return errors.WithStack(result.Error)
	})
}

// DeleteEnrollmentRule implements port.EnrollmentStore.
func (s *Store) DeleteEnrollmentRule(ctx context.Context, id model.EnrollmentRuleID) error {
	// The applications of the rule stay: they remember which accounts the
	// organization already enrolled.
	return s.recorded(ctx, tracking("enrollment_rule", string(id)), func(ctx context.Context, db *gorm.DB) error {
		result := db.Delete(&EnrollmentRule{}, "id = ?", string(id))
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		if result.RowsAffected == 0 {
			return errors.WithStack(port.ErrNotFound)
		}
		return nil
	})
}

// GetEnrollmentRuleByID implements port.EnrollmentStore.
func (s *Store) GetEnrollmentRuleByID(ctx context.Context, id model.EnrollmentRuleID) (model.EnrollmentRule, error) {
	var r EnrollmentRule
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		if err := db.First(&r, "id = ?", string(id)).Error; err != nil {
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
	return &wrappedEnrollmentRule{&r}, nil
}

// ListEnrollmentRules implements port.EnrollmentStore.
func (s *Store) ListEnrollmentRules(ctx context.Context, orgID model.OrgID) ([]model.EnrollmentRule, error) {
	var rows []*EnrollmentRule
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("org_id = ?", string(orgID)).Order("created_at, id").Find(&rows).Error)
	})
	if err != nil {
		return nil, err
	}
	return wrapEnrollmentRules(rows), nil
}

// ListActiveEnrollmentRules implements port.EnrollmentStore.
func (s *Store) ListActiveEnrollmentRules(ctx context.Context, tenantID model.TenantID) ([]model.EnrollmentRule, error) {
	var rows []*EnrollmentRule
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		active := db.Session(&gorm.Session{NewDB: true}).Model(&Organization{}).Select("id").
			Where("tenant_id = ? AND active <> 0", string(tenantID))
		return errors.WithStack(db.Where("enabled = ? AND org_id IN (?)", true, active).
			Where(notFrozen("enrollment_rules", "enrollment_rules")).
			Order("created_at, id").Find(&rows).Error)
	})
	if err != nil {
		return nil, err
	}
	return wrapEnrollmentRules(rows), nil
}

// ListAppliedEnrollmentRules implements port.EnrollmentStore.
func (s *Store) ListAppliedEnrollmentRules(ctx context.Context, userID model.UserID) ([]model.EnrollmentRuleID, error) {
	var ids []model.EnrollmentRuleID
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&EnrollmentApplication{}).Where("user_id = ?", string(userID)).Pluck("rule_id", &ids).Error)
	})
	return ids, err
}

// RecordEnrollmentApplication implements port.EnrollmentStore.
func (s *Store) RecordEnrollmentApplication(ctx context.Context, rule model.EnrollmentRule, userID model.UserID) (bool, error) {
	recorded := false
	err := s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&EnrollmentApplication{
			RuleID: string(rule.ID()), UserID: string(userID), OrgID: string(rule.OrgID()),
		})
		recorded = result.RowsAffected == 1
		return errors.WithStack(result.Error)
	})
	return recorded, err
}

// CountEnrollmentApplications implements port.EnrollmentStore.
func (s *Store) CountEnrollmentApplications(ctx context.Context, orgID model.OrgID, userID model.UserID) (int64, error) {
	var count int64
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&EnrollmentApplication{}).
			Where("org_id = ? AND user_id = ?", string(orgID), string(userID)).Count(&count).Error)
	})
	return count, err
}

// MarkEnrollmentActivationPending implements port.EnrollmentStore.
func (s *Store) MarkEnrollmentActivationPending(ctx context.Context, userID model.UserID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Clauses(clause.OnConflict{DoNothing: true}).Create(&EnrollmentPendingActivation{UserID: string(userID)}).Error)
	})
}

// IsEnrollmentActivationPending implements port.EnrollmentStore.
func (s *Store) IsEnrollmentActivationPending(ctx context.Context, userID model.UserID, since time.Time) (bool, error) {
	var count int64
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&EnrollmentPendingActivation{}).
			Where("user_id = ? AND created_at >= ?", string(userID), since).Count(&count).Error)
	})
	return count > 0, err
}

// ClearEnrollmentActivationPending implements port.EnrollmentStore.
func (s *Store) ClearEnrollmentActivationPending(ctx context.Context, userID model.UserID) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Delete(&EnrollmentPendingActivation{}, "user_id = ?", string(userID)).Error)
	})
}

func wrapEnrollmentRules(rows []*EnrollmentRule) []model.EnrollmentRule {
	rules := make([]model.EnrollmentRule, 0, len(rows))
	for _, r := range rows {
		rules = append(rules, &wrappedEnrollmentRule{r})
	}
	return rules
}

var _ port.EnrollmentStore = (*Store)(nil)
