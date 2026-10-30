package gorm

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
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
		row := fromUsageRecord(record)
		if err := db.Create(row).Error; err != nil {
			return errors.WithStack(err)
		}
		if err := incrementQuotaUsage(db, quotaUsageRows(record)); err != nil {
			return err
		}
		return incrementUsageDaily(db, row, record.Energy())
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

	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
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

	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
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
	type usageCounts struct {
		TotalRequests    int64
		TotalCost        int64
		PromptTokens     int64
		CachedTokens     int64
		CompletionTokens int64
		TotalTokens      int64
	}
	var counts usageCounts

	sources := usageSources(filter, time.Now())
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return readPeriod(db, sources, func(db *gorm.DB) error {
			// The store replays the whole read on a retryable error: each
			// attempt starts from zero, or the parts read twice add up twice.
			counts = usageCounts{}
			for _, src := range sources {
				requests := "COUNT(*)"
				if src.daily {
					requests = "COALESCE(SUM(requests),0)"
				}
				var part struct {
					TotalRequests    int64
					TotalCost        int64
					PromptTokens     int64
					CachedTokens     int64
					CompletionTokens int64
					TotalTokens      int64
				}
				query := src.query(db).
					Select(requests + " as total_requests, COALESCE(SUM(cost),0) as total_cost, COALESCE(SUM(prompt_tokens),0) as prompt_tokens, COALESCE(SUM(cached_tokens),0) as cached_tokens, COALESCE(SUM(completion_tokens),0) as completion_tokens, COALESCE(SUM(total_tokens),0) as total_tokens")
				if err := query.Scan(&part).Error; err != nil {
					return errors.WithStack(err)
				}
				counts.TotalRequests += part.TotalRequests
				counts.TotalCost += part.TotalCost
				counts.PromptTokens += part.PromptTokens
				counts.CachedTokens += part.CachedTokens
				counts.CompletionTokens += part.CompletionTokens
				counts.TotalTokens += part.TotalTokens
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	// Detect the currency from the most recent record matching the filter
	var currency string
	_ = s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
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

	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
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
	if filter.TeamID != nil {
		query = query.Where("team_id = ?", string(*filter.TeamID))
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
	payg := false
	filter := port.UsageFilter{OrgID: &orgID, UserIDs: userIDs, PlanCovered: &payg, Since: &since}
	sources := usageSources(filter, time.Now())

	var result map[string]int64
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return readPeriod(db, sources, func(db *gorm.DB) error {
			// Each attempt of a replayed read starts from zero.
			result = make(map[string]int64)
			for _, src := range sources {
				var rows []struct {
					Currency string
					Total    int64
				}
				query := src.query(db).Select("currency, COALESCE(SUM(cost), 0) as total").Group("currency")
				if err := query.Scan(&rows).Error; err != nil {
					return errors.WithStack(err)
				}
				for _, r := range rows {
					result[r.Currency] += r.Total
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// planUsageQuery selects the subscription-covered usage records of a plan
// scope since the given time. A per-model scope narrows them to its models;
// an empty one counts the whole provider.
func planUsageQuery(db *gorm.DB, scope model.PlanUsageScope, since time.Time) *gorm.DB {
	query := db.Model(&UsageRecord{}).
		Where("org_id = ? AND provider_id = ? AND plan_covered = 1 AND created_at >= ?",
			string(scope.OrgID), string(scope.ProviderID), scope.Since(since))
	if len(scope.KeyWindows) > 0 {
		// The range above starts at the earliest window, so the indexes keep
		// bounding it; each key is then held to its own start.
		keys := slices.Sorted(maps.Keys(scope.KeyWindows))
		ids := make([]string, len(keys))
		for i, key := range keys {
			ids[i] = string(key)
		}
		windows := db.Where("provider_key_id NOT IN ? AND created_at >= ?", ids, since)
		for _, key := range keys {
			windows = windows.Or("provider_key_id = ? AND created_at >= ?", string(key), scope.KeyWindows[key])
		}
		query = query.Where(windows)
	}
	if len(scope.ModelIDs) > 0 {
		ids := make([]string, len(scope.ModelIDs))
		for i, id := range scope.ModelIDs {
			ids[i] = string(id)
		}
		query = query.Where("model_id IN ?", ids)
	}
	if scope.ByKey {
		query = query.Where("provider_key_id = ?", string(scope.KeyID))
	}
	return query
}

// SumPlanUsageByKeySince implements port.UsageStore.
func (s *Store) SumPlanUsageByKeySince(ctx context.Context, scope model.PlanUsageScope, since time.Time) (map[model.ProviderKeyID]map[model.LLMModelID]port.PlanUsageTotals, error) {
	var rows []struct {
		ProviderKeyID string
		ModelID       string
		Tokens        int64
		ProviderValue int64
	}

	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(planUsageQuery(db, scope, since).
			Select("provider_key_id as provider_key_id, model_id as model_id, COALESCE(SUM(total_tokens), 0) as tokens, COALESCE(SUM(provider_cost), 0) as provider_value").
			Group("provider_key_id, model_id").
			Scan(&rows).Error)
	})
	if err != nil {
		return nil, err
	}

	result := make(map[model.ProviderKeyID]map[model.LLMModelID]port.PlanUsageTotals)
	for _, r := range rows {
		key := model.ProviderKeyID(r.ProviderKeyID)
		if result[key] == nil {
			result[key] = make(map[model.LLMModelID]port.PlanUsageTotals)
		}
		result[key][model.LLMModelID(r.ModelID)] = port.PlanUsageTotals{Tokens: r.Tokens, Value: r.ProviderValue}
	}
	return result, nil
}

// LastPlanUsageKey implements port.UsageStore.
func (s *Store) LastPlanUsageKey(ctx context.Context, scope model.PlanUsageScope, userID model.UserID) (model.ProviderKeyID, bool, error) {
	var keys []string

	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		// Walks idx_usage_org_prov_user backwards from the user's newest entry
		// and stops there.
		return errors.WithStack(db.Model(&UsageRecord{}).
			Where("org_id = ? AND provider_id = ? AND plan_covered = 1 AND user_id = ?",
				string(scope.OrgID), string(scope.ProviderID), string(userID)).
			Order("created_at DESC").
			Limit(1).
			Pluck("provider_key_id", &keys).Error)
	})
	if err != nil {
		return "", false, err
	}
	if len(keys) == 0 {
		return "", false, nil
	}
	return model.ProviderKeyID(keys[0]), true, nil
}

// SumPlanUsageSince implements port.UsageStore.
func (s *Store) SumPlanUsageSince(ctx context.Context, scope model.PlanUsageScope, since time.Time) (tokens int64, providerValue int64, err error) {
	var row struct {
		Tokens        int64
		ProviderValue int64
	}

	err = s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(planUsageQuery(db, scope, since).
			Select("COALESCE(SUM(total_tokens), 0) as tokens, COALESCE(SUM(provider_cost), 0) as provider_value").
			Scan(&row).Error)
	})
	if err != nil {
		return 0, 0, err
	}
	return row.Tokens, row.ProviderValue, nil
}

// SumPlanUsageByModelSince implements port.UsageStore.
func (s *Store) SumPlanUsageByModelSince(ctx context.Context, scope model.PlanUsageScope, userID model.UserID, since time.Time) (map[model.LLMModelID]port.PlanUsageTotals, error) {
	var rows []struct {
		ModelID       string
		Tokens        int64
		ProviderValue int64
	}

	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		query := planUsageQuery(db, scope, since).
			Select("model_id as model_id, COALESCE(SUM(total_tokens), 0) as tokens, COALESCE(SUM(provider_cost), 0) as provider_value")
		if userID != "" {
			query = query.Where("user_id = ?", string(userID))
		}
		return errors.WithStack(query.Group("model_id").Scan(&rows).Error)
	})
	if err != nil {
		return nil, err
	}

	result := make(map[model.LLMModelID]port.PlanUsageTotals, len(rows))
	for _, r := range rows {
		result[model.LLMModelID(r.ModelID)] = port.PlanUsageTotals{Tokens: r.Tokens, Value: r.ProviderValue}
	}
	return result, nil
}

// SumUserPlanUsageSince implements port.UsageStore.
func (s *Store) SumUserPlanUsageSince(ctx context.Context, userID model.UserID, scope model.PlanUsageScope, since time.Time) (tokens int64, providerValue int64, err error) {
	var row struct {
		Tokens        int64
		ProviderValue int64
	}

	err = s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(planUsageQuery(db, scope, since).
			Select("COALESCE(SUM(total_tokens), 0) as tokens, COALESCE(SUM(provider_cost), 0) as provider_value").
			Where("user_id = ?", string(userID)).
			Scan(&row).Error)
	})
	if err != nil {
		return 0, 0, err
	}
	return row.Tokens, row.ProviderValue, nil
}

// CountActivePlanUsersSince implements port.UsageStore.
func (s *Store) CountActivePlanUsersSince(ctx context.Context, scope model.PlanUsageScope, since time.Time, excludeUserID model.UserID) (int64, error) {
	var count int64

	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		query := planUsageQuery(db, scope, since).
			Select("COUNT(DISTINCT user_id)").
			Where("user_id <> ''")
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
func (s *Store) HasPlanUsageSince(ctx context.Context, userID model.UserID, scope model.PlanUsageScope, since time.Time) (bool, error) {
	var ids []string

	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		// A probe the planner can stop at the first hit. Not COUNT with a LIMIT:
		// GORM keeps the LIMIT but turns the SELECT into count(*), which
		// aggregates every matching row before the LIMIT applies to the result.
		// On a provider-wide scope the predicate is served by
		// idx_usage_org_prov_user, whose user_id sits before the created_at range
		// so the equality bounds the index.
		return errors.WithStack(planUsageQuery(db, scope, since).
			Where("user_id = ?", string(userID)).
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
		// record near midnight lands in; never the summed totals. A period read
		// partly from usage_daily buckets its records otherwise, see
		// usageSource.dimensionExpr.
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
	byDimension, err := s.AggregateCostByDimensions(ctx, filter, dimension)
	if err != nil {
		return nil, err
	}
	return byDimension[dimension], nil
}

// maxUsageDimensions is how many dimensions AggregateCostByDimensions groups
// on at once: one key column each, Key0 to Key3 of its rows.
const maxUsageDimensions = 4

// AggregateCostByDimensions implements port.UsageStore.
//
// The query groups on every dimension at once, then each dimension's
// sub-totals are summed from those rows. There is one row per distinct
// combination of the dimensions (with org, currency and plan coverage): their
// product, not their sum. On a long range of a busy organization it can come
// close to the number of records, and all of it is loaded before summing.
func (s *Store) AggregateCostByDimensions(ctx context.Context, filter port.UsageFilter, dimensions ...port.UsageDimension) (map[port.UsageDimension][]port.DimensionCost, error) {
	dimensions = slices.Compact(slices.Sorted(slices.Values(dimensions)))
	if len(dimensions) == 0 {
		return map[port.UsageDimension][]port.DimensionCost{}, nil
	}
	if len(dimensions) > maxUsageDimensions {
		return nil, errors.Errorf("at most %d usage dimensions at once, got %v", maxUsageDimensions, dimensions)
	}

	type costRow struct {
		Key0, Key1, Key2, Key3 string
		OrgID                  string
		Currency               string
		PlanCovered            int
		Cost                   int64
	}
	// The rows of every source of the period, summed below like the rows of
	// one query.
	var rows []costRow
	sources := usageSources(filter, time.Now())
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return readPeriod(db, sources, func(db *gorm.DB) error {
			// The store replays the whole read on a retryable error: each
			// attempt starts from no rows, or the parts read twice add up twice.
			rows = nil
			for _, src := range sources {
				selects := make([]string, 0, len(dimensions)+4)
				groups := make([]string, 0, len(dimensions))
				for i, dimension := range dimensions {
					groupExpr, err := src.dimensionExpr(db, dimension)
					if err != nil {
						return err
					}
					selects = append(selects, fmt.Sprintf("%s as key%d", groupExpr, i))
					groups = append(groups, groupExpr)
				}
				selects = append(selects, "org_id as org_id", "currency as currency", "plan_covered as plan_covered", "COALESCE(SUM(cost), 0) as cost")

				query := src.query(db).Select(strings.Join(selects, ", "))
				for _, group := range groups {
					query = query.Group(group)
				}
				query = query.Group("org_id").Group("currency").Group("plan_covered")

				var part []costRow
				if err := query.Scan(&part).Error; err != nil {
					return errors.WithStack(err)
				}
				if day := slices.Index(dimensions, port.UsageDimensionDay); day >= 0 && src.quarterDays {
					for j := range part {
						keys := [maxUsageDimensions]*string{&part[j].Key0, &part[j].Key1, &part[j].Key2, &part[j].Key3}
						localDay, err := localDayOfQuarter(*keys[day])
						if err != nil {
							return err
						}
						*keys[day] = localDay
					}
				}
				rows = append(rows, part...)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	result := make(map[port.UsageDimension][]port.DimensionCost, len(dimensions))
	for i, dimension := range dimensions {
		// Sub-totals are indexed on everything but their cost.
		index := make(map[port.DimensionCost]int)
		costs := make([]port.DimensionCost, 0)
		for _, row := range rows {
			subTotal := port.DimensionCost{
				Key:         [maxUsageDimensions]string{row.Key0, row.Key1, row.Key2, row.Key3}[i],
				OrgID:       model.OrgID(row.OrgID),
				Currency:    row.Currency,
				PlanCovered: row.PlanCovered != 0,
			}
			k, ok := index[subTotal]
			if !ok {
				k = len(costs)
				index[subTotal] = k
				costs = append(costs, subTotal)
			}
			costs[k].Cost += row.Cost
		}
		result[dimension] = costs
	}
	return result, nil
}

// AggregatePlanTokensByUser implements port.UsageStore.
func (s *Store) AggregatePlanTokensByUser(ctx context.Context, filter port.UsageFilter) ([]port.UserTokenUsage, error) {
	covered := true
	filter.PlanCovered = &covered
	sources := usageSources(filter, time.Now())

	var tokens map[string]int64
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return readPeriod(db, sources, func(db *gorm.DB) error {
			// Each attempt of a replayed read starts from zero.
			tokens = make(map[string]int64)
			for _, src := range sources {
				var rows []struct {
					UserID string
					Tokens int64
				}
				query := src.query(db).Select("user_id as user_id, COALESCE(SUM(total_tokens), 0) as tokens").Group("user_id")
				if err := query.Scan(&rows).Error; err != nil {
					return errors.WithStack(err)
				}
				for _, r := range rows {
					tokens[r.UserID] += r.Tokens
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	result := make([]port.UserTokenUsage, 0, len(tokens))
	for userID, sum := range tokens {
		result = append(result, port.UserTokenUsage{UserID: model.UserID(userID), Tokens: sum})
	}
	return result, nil
}

// AggregateEnergy implements port.UsageStore.
func (s *Store) AggregateEnergy(ctx context.Context, filter port.UsageFilter) (model.UsageEnergy, []port.RequestSize, error) {
	type sizeKey struct {
		modelID                        string
		promptTokens, completionTokens int64
	}
	var (
		stored model.UsageEnergy
		sizes  map[sizeKey]int64
	)
	sources := usageSources(filter, time.Now())
	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		return readPeriod(db, sources, func(db *gorm.DB) error {
			// Each attempt of a replayed read starts from zero.
			stored, sizes = model.UsageEnergy{}, make(map[sizeKey]int64)
			for _, src := range sources {
				if src.daily {
					var part struct{ Wh, CO2 float64 }
					query := src.query(db).Select("COALESCE(SUM(energy_wh), 0) as wh, COALESCE(SUM(co2_grams), 0) as co2")
					if err := query.Scan(&part).Error; err != nil {
						return errors.WithStack(err)
					}
					stored.Wh += part.Wh
					stored.CO2Grams += part.CO2
					continue
				}
				var rows []struct {
					ModelID          string
					PromptTokens     int64
					CompletionTokens int64
					Requests         int64
				}
				query := src.query(db).
					Select("model_id as model_id, prompt_tokens as prompt_tokens, completion_tokens as completion_tokens, COUNT(*) as requests").
					Where("model_id <> ''").
					Group("model_id, prompt_tokens, completion_tokens")
				if err := query.Scan(&rows).Error; err != nil {
					return errors.WithStack(err)
				}
				for _, r := range rows {
					sizes[sizeKey{r.ModelID, r.PromptTokens, r.CompletionTokens}] += r.Requests
				}
			}
			return nil
		})
	})
	if err != nil {
		return model.UsageEnergy{}, nil, err
	}

	result := make([]port.RequestSize, 0, len(sizes))
	for k, requests := range sizes {
		result = append(result, port.RequestSize{
			ModelID:          model.LLMModelID(k.modelID),
			PromptTokens:     k.promptTokens,
			CompletionTokens: k.completionTokens,
			Requests:         requests,
		})
	}
	return stored, result, nil
}

// AggregateRequestSizes implements port.UsageStore.
func (s *Store) AggregateRequestSizes(ctx context.Context, filter port.UsageFilter) ([]port.RequestSize, error) {
	var rows []struct {
		ModelID          string
		PromptTokens     int64
		CompletionTokens int64
		Requests         int64
	}

	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		query := db.Model(&UsageRecord{}).
			Select("model_id as model_id, prompt_tokens as prompt_tokens, completion_tokens as completion_tokens, COUNT(*) as requests").
			Where("model_id <> ''")
		query = applyUsageFilter(query, filter)
		query = query.Group("model_id, prompt_tokens, completion_tokens")
		return errors.WithStack(query.Scan(&rows).Error)
	})
	if err != nil {
		return nil, err
	}

	result := make([]port.RequestSize, 0, len(rows))
	for _, r := range rows {
		result = append(result, port.RequestSize{
			ModelID:          model.LLMModelID(r.ModelID),
			PromptTokens:     r.PromptTokens,
			CompletionTokens: r.CompletionTokens,
			Requests:         r.Requests,
		})
	}
	return result, nil
}

// EarliestPlanUsageSince implements port.UsageStore.
func (s *Store) EarliestPlanUsageSince(ctx context.Context, scope model.PlanUsageScope, userID model.UserID, since time.Time) (time.Time, error) {
	var row struct {
		Earliest *time.Time
	}

	err := s.read(ctx, func(ctx context.Context, db *gorm.DB) error {
		query := planUsageQuery(db, scope, since).
			Select("MIN(created_at) as earliest")
		if userID != "" {
			query = query.Where("user_id = ?", string(userID))
		}
		return errors.WithStack(query.Scan(&row).Error)
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
