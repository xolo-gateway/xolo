package gorm

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SetQuota implements port.QuotaStore.
func (s *Store) setQuota(ctx context.Context, quota model.Quota) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "scope"}, {Name: "scope_id"}},
			UpdateAll: true,
		}).Create(fromQuota(quota)).Error)
	})
}

// GetQuota implements port.QuotaStore.
func (s *Store) GetQuota(ctx context.Context, scope model.QuotaScope, scopeID string) (model.Quota, error) {
	var q Quota
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Where("scope = ? AND scope_id = ?", string(scope), scopeID).First(&q).Error; err != nil {
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
	return &wrappedQuota{&q}, nil
}

// ResolveEffectiveQuota implements port.QuotaStore.
// Takes the minimum non-nil budget at each period across user and org quotas.
// The second return value is the raw org quota the resolver loaded to feed the
// merge: callers (the budget enforcer, mostly) reuse it instead of issuing a
// second GetQuota on the hot path. nil means "no org quota on file", same
// shape as a GetQuota that returned port.ErrNotFound (issue #82).
func (s *Store) ResolveEffectiveQuota(ctx context.Context, userID model.UserID, orgID model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	effective := &model.EffectiveQuota{}

	userQuota, err := s.GetQuota(ctx, model.QuotaScopeUser, string(userID))
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		return nil, nil, errors.WithStack(err)
	}

	orgQuota, err := s.GetQuota(ctx, model.QuotaScopeOrg, string(orgID))
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		return nil, nil, errors.WithStack(err)
	}

	// Currency: org quota takes precedence; fall back to user quota, then default.
	switch {
	case orgQuota != nil:
		effective.Currency = orgQuota.Currency()
	case userQuota != nil:
		effective.Currency = userQuota.Currency()
	default:
		effective.Currency = model.DefaultCurrency
	}

	// Merge budgets from both levels, taking the stricter (minimum) non-nil value.
	// Currency labels are intentionally ignored here: since quotas are always saved
	// using the org's currency, budget values are in the same unit regardless of the
	// currency field stored on each record (which may be stale from an earlier org
	// currency setting).
	effective.DailyBudget = minPtr(quotaDaily(userQuota), quotaDaily(orgQuota))
	effective.MonthlyBudget = minPtr(quotaMonthly(userQuota), quotaMonthly(orgQuota))
	effective.YearlyBudget = minPtr(quotaYearly(userQuota), quotaYearly(orgQuota))

	return effective, orgQuota, nil
}

// ResolveEffectiveQuotaForApplication implements port.QuotaStore.
// Takes the minimum non-nil budget at each period across application and org quotas.
// The second return value is the raw org quota the resolver loaded to feed the
// merge, for the same dedup reason as ResolveEffectiveQuota (issue #82).
func (s *Store) ResolveEffectiveQuotaForApplication(ctx context.Context, appID model.ApplicationID, orgID model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	effective := &model.EffectiveQuota{}

	appQuota, err := s.GetQuota(ctx, model.QuotaScopeApplication, string(appID))
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		return nil, nil, errors.WithStack(err)
	}

	orgQuota, err := s.GetQuota(ctx, model.QuotaScopeOrg, string(orgID))
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		return nil, nil, errors.WithStack(err)
	}

	switch {
	case orgQuota != nil:
		effective.Currency = orgQuota.Currency()
	case appQuota != nil:
		effective.Currency = appQuota.Currency()
	default:
		effective.Currency = model.DefaultCurrency
	}

	effective.DailyBudget = minPtr(quotaDaily(appQuota), quotaDaily(orgQuota))
	effective.MonthlyBudget = minPtr(quotaMonthly(appQuota), quotaMonthly(orgQuota))
	effective.YearlyBudget = minPtr(quotaYearly(appQuota), quotaYearly(orgQuota))

	return effective, orgQuota, nil
}

func quotaDaily(q model.Quota) *int64 {
	if q == nil {
		return nil
	}
	return q.DailyBudget()
}

func quotaMonthly(q model.Quota) *int64 {
	if q == nil {
		return nil
	}
	return q.MonthlyBudget()
}

func quotaYearly(q model.Quota) *int64 {
	if q == nil {
		return nil
	}
	return q.YearlyBudget()
}

// minPtr returns the minimum of two *int64. nil means "no limit".
// If both are nil → nil (no limit).
// If one is nil → the non-nil one (a concrete limit is stricter than none).
func minPtr(a, b *int64) *int64 {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if *a < *b {
		return a
	}
	return b
}

var _ port.QuotaStore = &Store{}

func (s *Store) SetQuota(ctx context.Context, quota model.Quota) error {
	return s.identityTransaction(ctx, func(tx *Store) error {
		row := fromQuota(quota)
		old, err := tx.GetQuota(ctx, quota.Scope(), quota.ScopeID())
		if err != nil && !errors.Is(err, port.ErrNotFound) {
			return err
		}
		if err == nil {
			row.ID = string(old.ID())
		}
		return tx.mutate(ctx, "quota", row.ID, func(bound *Store) error { return bound.setQuota(ctx, &wrappedQuota{row}) })
	})
}
