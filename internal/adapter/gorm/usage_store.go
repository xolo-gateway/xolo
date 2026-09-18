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

// RecordUsage implements port.UsageStore.
//
// The record and the quota counters it feeds are written in the same
// transaction: a counter that missed an insert would under-report spending
// until the next backfill, and there is no backfill on the hot path.
func (s *Store) RecordUsage(ctx context.Context, record model.UsageRecord) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Create(fromUsageRecord(record)).Error; err != nil {
			return errors.WithStack(err)
		}
		return incrementQuotaUsage(db, quotaUsageRows(record))
	})
}

// incrementQuotaUsage adds each row's cost to the matching counter, creating it
// on first use. The upsert is a single statement per row so two concurrent
// requests on the same scope add up instead of overwriting each other.
func incrementQuotaUsage(db *gorm.DB, rows []*QuotaUsage) error {
	for _, row := range rows {
		err := db.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "scope"}, {Name: "scope_id"}, {Name: "org_id"}, {Name: "currency"}, {Name: "day"},
			},
			// The column is qualified by the table so it names the stored row and
			// not the one being inserted: this adds to the existing total rather
			// than replacing it. PostgreSQL rejects the unqualified form outright
			// ("column reference \"cost\" is ambiguous").
			DoUpdates: clause.Assignments(map[string]any{
				"cost": gorm.Expr(quotaUsageTable+".cost + ?", row.Cost),
			}),
		}).Create(row).Error
		if err != nil {
			return errors.WithStack(err)
		}
	}
	return nil
}

// SumQuotaCostSince implements port.UsageStore.
func (s *Store) SumQuotaCostSince(ctx context.Context, scope model.QuotaScope, scopeID string, orgID model.OrgID, since time.Time) (int64, error) {
	var total int64

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		var result struct{ Total int64 }
		err := db.Model(&QuotaUsage{}).
			Select("COALESCE(SUM(cost), 0) as total").
			Where("scope = ? AND scope_id = ? AND org_id = ? AND day >= ?", string(scope), scopeID, string(orgID), quotaUsageDay(since)).
			Scan(&result).Error
		total = result.Total
		return errors.WithStack(err)
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// QueryUsage implements port.UsageStore.
func (s *Store) QueryUsage(ctx context.Context, filter port.UsageFilter) ([]model.UsageRecord, error) {
	var records []*UsageRecord

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&UsageRecord{})
		query = applyUsageFilter(query, filter)
		query = query.Order("created_at DESC")
		if filter.Limit != nil {
			query = query.Limit(*filter.Limit)
		}
		if filter.Offset != nil {
			query = query.Offset(*filter.Offset)
		}
		return errors.WithStack(query.Find(&records).Error)
	})
	if err != nil {
		return nil, err
	}

	result := make([]model.UsageRecord, 0, len(records))
	for _, r := range records {
		result = append(result, &wrappedUsageRecord{r})
	}
	return result, nil
}

// AggregateUsage implements port.UsageStore.
func (s *Store) AggregateUsage(ctx context.Context, filter port.UsageFilter) (*port.UsageAggregate, error) {
	var counts struct {
		TotalRequests    int64
		TotalCost        int64
		PromptTokens     int64
		CachedTokens     int64
		CompletionTokens int64
		TotalTokens      int64
	}

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&UsageRecord{}).
			Select("COUNT(*) as total_requests, COALESCE(SUM(cost),0) as total_cost, COALESCE(SUM(prompt_tokens),0) as prompt_tokens, COALESCE(SUM(cached_tokens),0) as cached_tokens, COALESCE(SUM(completion_tokens),0) as completion_tokens, COALESCE(SUM(total_tokens),0) as total_tokens")
		query = applyUsageFilter(query, filter)
		return errors.WithStack(query.Scan(&counts).Error)
	})
	if err != nil {
		return nil, err
	}

	// Detect the currency from the most recent record matching the filter
	var currency string
	_ = s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		var row struct{ Currency string }
		query := db.Model(&UsageRecord{}).Select("currency").Order("created_at DESC")
		query = applyUsageFilter(query, filter)
		query.Limit(1).Scan(&row)
		currency = row.Currency
		return nil
	})

	return &port.UsageAggregate{
		TotalRequests:    counts.TotalRequests,
		TotalCost:        counts.TotalCost,
		Currency:         currency,
		PromptTokens:     counts.PromptTokens,
		CachedTokens:     counts.CachedTokens,
		CompletionTokens: counts.CompletionTokens,
		TotalTokens:      counts.TotalTokens,
	}, nil
}

// SumCostSince implements port.UsageStore.
// Only PAYG (plan_covered=0) records are included so subscription usage does not
// inflate monetary quotas.
func (s *Store) SumCostSince(ctx context.Context, userID model.UserID, orgID model.OrgID, since time.Time) (int64, error) {
	var total int64

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		var result struct{ Total int64 }
		err := db.Model(&UsageRecord{}).
			Select("COALESCE(SUM(cost), 0) as total").
			Where("user_id = ? AND org_id = ? AND created_at >= ? AND plan_covered = 0", string(userID), string(orgID), since).
			Scan(&result).Error
		total = result.Total
		return errors.WithStack(err)
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// principalIDs merges the single-value and slice forms of a filter field into
// one list of string ids, dropping duplicates.
func principalIDs[T ~string](single *T, many []T) []string {
	ids := make([]string, 0, len(many)+1)
	seen := make(map[string]struct{}, len(many)+1)

	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	if single != nil {
		add(string(*single))
	}
	for _, id := range many {
		add(string(id))
	}

	return ids
}

func applyUsageFilter(query *gorm.DB, filter port.UsageFilter) *gorm.DB {
	userIDs := principalIDs(filter.UserID, filter.UserIDs)
	appIDs := principalIDs(filter.ApplicationID, filter.ApplicationIDs)

	// A usage record is attributed to either a user or an application, never
	// both, so filtering on both kinds at once means "any of these principals".
	switch {
	case len(userIDs) > 0 && len(appIDs) > 0:
		query = query.Where("user_id IN ? OR application_id IN ?", userIDs, appIDs)
	case len(userIDs) > 0:
		query = query.Where("user_id IN ?", userIDs)
	case len(appIDs) > 0:
		query = query.Where("application_id IN ?", appIDs)
	}
	if filter.OrgID != nil {
		query = query.Where("org_id = ?", string(*filter.OrgID))
	}
	if filter.ProviderID != nil {
		query = query.Where("provider_id = ?", string(*filter.ProviderID))
	}
	if filter.ModelID != nil {
		query = query.Where("model_id = ?", string(*filter.ModelID))
	}
	if filter.PlanCovered != nil {
		if *filter.PlanCovered {
			query = query.Where("plan_covered = 1")
		} else {
			query = query.Where("plan_covered = 0")
		}
	}
	if filter.AuthTokenID != nil {
		query = query.Where("auth_token_id = ?", *filter.AuthTokenID)
	}
	if filter.Currency != nil {
		query = query.Where("currency = ?", *filter.Currency)
	}
	if filter.ProxyModelName != nil {
		query = query.Where("proxy_model_name = ?", *filter.ProxyModelName)
	}
	if filter.Since != nil {
		query = query.Where("created_at >= ?", *filter.Since)
	}
	if filter.Until != nil {
		query = query.Where("created_at <= ?", *filter.Until)
	}
	return query
}

// SumCostSinceByCurrency implements port.UsageStore.
// Only PAYG (plan_covered=0) records are included so subscription usage does not
// inflate monetary quotas.
func (s *Store) SumCostSinceByCurrency(ctx context.Context, userIDs []model.UserID, orgID model.OrgID, since time.Time) (map[string]int64, error) {
	var rows []struct {
		Currency string
		Total    int64
	}

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&UsageRecord{}).
			Select("currency, COALESCE(SUM(cost), 0) as total").
			Where("org_id = ? AND created_at >= ? AND plan_covered = 0", string(orgID), since)
		if len(userIDs) > 0 {
			ids := make([]string, len(userIDs))
			for i, uid := range userIDs {
				ids[i] = string(uid)
			}
			query = query.Where("user_id IN ?", ids)
		}
		return errors.WithStack(query.Group("currency").Scan(&rows).Error)
	})
	if err != nil {
		return nil, err
	}

	result := make(map[string]int64, len(rows))
	for _, r := range rows {
		result[r.Currency] = r.Total
	}
	return result, nil
}

// SumPlanUsageSince implements port.UsageStore.
func (s *Store) SumPlanUsageSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time) (tokens int64, providerValue int64, err error) {
	var row struct {
		Tokens        int64
		ProviderValue int64
	}

	err = s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&UsageRecord{}).
			Select("COALESCE(SUM(total_tokens), 0) as tokens, COALESCE(SUM(provider_cost), 0) as provider_value").
			Where("org_id = ? AND provider_id = ? AND created_at >= ? AND plan_covered = 1",
				string(orgID), string(providerID), since).
			Scan(&row).Error)
	})
	if err != nil {
		return 0, 0, err
	}
	return row.Tokens, row.ProviderValue, nil
}

// SumUserPlanUsageSince implements port.UsageStore.
func (s *Store) SumUserPlanUsageSince(ctx context.Context, userID model.UserID, orgID model.OrgID, providerID model.ProviderID, since time.Time) (tokens int64, providerValue int64, err error) {
	var row struct {
		Tokens        int64
		ProviderValue int64
	}

	err = s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&UsageRecord{}).
			Select("COALESCE(SUM(total_tokens), 0) as tokens, COALESCE(SUM(provider_cost), 0) as provider_value").
			Where("user_id = ? AND org_id = ? AND provider_id = ? AND created_at >= ? AND plan_covered = 1",
				string(userID), string(orgID), string(providerID), since).
			Scan(&row).Error)
	})
	if err != nil {
		return 0, 0, err
	}
	return row.Tokens, row.ProviderValue, nil
}

// CountActivePlanUsersSince implements port.UsageStore.
func (s *Store) CountActivePlanUsersSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time, excludeUserID model.UserID) (int64, error) {
	var count int64

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&UsageRecord{}).
			Select("COUNT(DISTINCT user_id)").
			Where("org_id = ? AND provider_id = ? AND created_at >= ? AND plan_covered = 1 AND user_id <> ''",
				string(orgID), string(providerID), since)
		if excludeUserID != "" {
			query = query.Where("user_id <> ?", string(excludeUserID))
		}
		return errors.WithStack(query.Scan(&count).Error)
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// HasPlanUsageSince implements port.UsageStore.
func (s *Store) HasPlanUsageSince(ctx context.Context, userID model.UserID, orgID model.OrgID, providerID model.ProviderID, since time.Time) (bool, error) {
	var ids []string

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		// A probe the planner can stop at the first hit. Not COUNT with a LIMIT:
		// GORM keeps the LIMIT but turns the SELECT into count(*), which
		// aggregates every matching row before the LIMIT applies to the result.
		// The predicate is served by idx_usage_org_prov_user, whose user_id sits
		// before the created_at range so the equality bounds the index.
		return errors.WithStack(db.Model(&UsageRecord{}).
			Where("org_id = ? AND provider_id = ? AND plan_covered = 1 AND user_id = ? AND created_at >= ?",
				string(orgID), string(providerID), string(userID), since).
			Limit(1).
			Pluck("id", &ids).Error)
	})
	if err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}

// dimensionGroupExpr returns the SQL expression to GROUP BY for a usage
// dimension, spelled for the backend db talks to.
func dimensionGroupExpr(db *gorm.DB, d port.UsageDimension) (string, error) {
	switch d {
	case port.UsageDimensionDay:
		// Bucket on the server's local calendar day, matching the time.Time
		// formatting used elsewhere, and render it as a YYYY-MM-DD string so
		// both backends yield the same key type. Only affects which day a
		// record near midnight lands in; never the summed totals.
		if isPostgres(db) {
			return "to_char(created_at AT TIME ZONE current_setting('TIMEZONE'), 'YYYY-MM-DD')", nil
		}
		return "date(created_at, 'localtime')", nil
	case port.UsageDimensionModel:
		return "COALESCE(NULLIF(resolved_model_name, ''), proxy_model_name)", nil
	case port.UsageDimensionUser:
		return "user_id", nil
	case port.UsageDimensionProvider:
		return "provider_id", nil
	default:
		return "", errors.Errorf("unsupported usage dimension %q", d)
	}
}

// AggregateCostByDimension implements port.UsageStore.
func (s *Store) AggregateCostByDimension(ctx context.Context, filter port.UsageFilter, dimension port.UsageDimension) ([]port.DimensionCost, error) {
	var rows []struct {
		GroupKey string
		OrgID    string
		Currency string
		Cost     int64
	}

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		groupExpr, err := dimensionGroupExpr(db, dimension)
		if err != nil {
			return err
		}

		query := db.Model(&UsageRecord{}).
			Select(groupExpr + " as group_key, org_id as org_id, currency as currency, COALESCE(SUM(cost), 0) as cost").
			Where("plan_covered = 0")
		query = applyUsageFilter(query, filter)
		query = query.Group(groupExpr).Group("org_id").Group("currency")
		return errors.WithStack(query.Scan(&rows).Error)
	})
	if err != nil {
		return nil, err
	}

	result := make([]port.DimensionCost, 0, len(rows))
	for _, r := range rows {
		result = append(result, port.DimensionCost{
			Key:      r.GroupKey,
			OrgID:    model.OrgID(r.OrgID),
			Currency: r.Currency,
			Cost:     r.Cost,
		})
	}
	return result, nil
}

// AggregatePlanTokensByUser implements port.UsageStore.
func (s *Store) AggregatePlanTokensByUser(ctx context.Context, filter port.UsageFilter) ([]port.UserTokenUsage, error) {
	var rows []struct {
		UserID string
		Tokens int64
	}

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&UsageRecord{}).
			Select("user_id as user_id, COALESCE(SUM(total_tokens), 0) as tokens").
			Where("plan_covered = 1")
		query = applyUsageFilter(query, filter)
		query = query.Group("user_id")
		return errors.WithStack(query.Scan(&rows).Error)
	})
	if err != nil {
		return nil, err
	}

	result := make([]port.UserTokenUsage, 0, len(rows))
	for _, r := range rows {
		result = append(result, port.UserTokenUsage{
			UserID: model.UserID(r.UserID),
			Tokens: r.Tokens,
		})
	}
	return result, nil
}

// EarliestPlanUsageSince implements port.UsageStore.
func (s *Store) EarliestPlanUsageSince(ctx context.Context, orgID model.OrgID, providerID model.ProviderID, since time.Time) (time.Time, error) {
	var row struct {
		Earliest *time.Time
	}

	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&UsageRecord{}).
			Select("MIN(created_at) as earliest").
			Where("org_id = ? AND provider_id = ? AND created_at >= ? AND plan_covered = 1",
				string(orgID), string(providerID), since).
			Scan(&row).Error)
	})
	if err != nil {
		return time.Time{}, err
	}
	if row.Earliest == nil {
		return time.Time{}, nil
	}
	return *row.Earliest, nil
}

var _ port.UsageStore = &Store{}
