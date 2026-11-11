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
)

// planUsageSplit is a plan scope's usage on one key and model, and the part
// of it the memo's user consumed.
type planUsageSplit struct {
	ProviderKeyID string
	ModelID       string
	Tokens        int64
	ProviderValue int64
	UserTokens    int64
	UserValue     int64
	// UserRows counts the rows of the user, so that a model the user used
	// without a billable token is listed, as a read for the user lists it.
	UserRows int64
}

// memoizedPlanUsage returns the usage of a plan scope since a time, split by
// key and model with the part of the memo's user, from the request's memo
// (see port.PlanUsageMemo), reading it on the first call. It reports false
// when the context carries no memo, or when userID is set and is not the
// memo's user: the caller then reads as usual.
//
// The scope is read without its key: the reads narrowed to one key of a pool
// and the reads of the whole pool share one entry, filtered by the caller.
func (s *Store) memoizedPlanUsage(ctx context.Context, scope model.PlanUsageScope, userID model.UserID, since time.Time) ([]planUsageSplit, bool, error) {
	memo := port.PlanUsageMemoFrom(ctx)
	if memo == nil || (userID != "" && userID != memo.UserID) {
		return nil, false, nil
	}
	pool := scope
	pool.ByKey, pool.KeyID = false, ""
	key := planUsageMemoKey(pool, since)
	if rows, ok := memo.Load(key); ok {
		return rows.([]planUsageSplit), true, nil
	}

	user := "0 as user_tokens, 0 as user_value, 0 as user_rows"
	var args []any
	if memo.UserID != "" {
		user = "COALESCE(SUM(CASE WHEN user_id = ? THEN total_tokens ELSE 0 END), 0) as user_tokens, " +
			"COALESCE(SUM(CASE WHEN user_id = ? THEN provider_cost ELSE 0 END), 0) as user_value, " +
			"COALESCE(SUM(CASE WHEN user_id = ? THEN 1 ELSE 0 END), 0) as user_rows"
		args = []any{string(memo.UserID), string(memo.UserID), string(memo.UserID)}
	}
	var rows []planUsageSplit
	err := s.readPlanUsage(ctx, pool, since, nil, func(usage *gorm.DB) error {
		rows = rows[:0]
		return errors.WithStack(usage.
			Select("provider_key_id as provider_key_id, model_id as model_id, "+planUsageSums+", "+user, args...).
			Group("provider_key_id, model_id").
			Scan(&rows).Error)
	})
	if err != nil {
		return nil, false, err
	}
	memo.Store(key, rows)
	return rows, true, nil
}

// planUsageMemoKey identifies a plan scope's window in a memo.
func planUsageMemoKey(scope model.PlanUsageScope, since time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%d|", scope.OrgID, scope.ProviderID, since.UnixNano())
	models := make([]string, len(scope.ModelIDs))
	for i, id := range scope.ModelIDs {
		models[i] = string(id)
	}
	slices.Sort(models)
	b.WriteString(strings.Join(models, ","))
	for _, k := range slices.Sorted(maps.Keys(scope.KeyWindows)) {
		fmt.Fprintf(&b, "|%s=%d", k, scope.KeyWindows[k].UnixNano())
	}
	return b.String()
}

// splitsOf keeps the splits of a scope's key, or all of them when the scope
// is not narrowed to one.
func splitsOf(scope model.PlanUsageScope, rows []planUsageSplit) func(yield func(planUsageSplit) bool) {
	return func(yield func(planUsageSplit) bool) {
		for _, r := range rows {
			if scope.ByKey && r.ProviderKeyID != string(scope.KeyID) {
				continue
			}
			if !yield(r) {
				return
			}
		}
	}
}

// totals is a split's usage, the plan's or, when user is set, the user's.
func (r planUsageSplit) totals(user bool) port.PlanUsageTotals {
	if user {
		return port.PlanUsageTotals{Tokens: r.UserTokens, Value: r.UserValue}
	}
	return port.PlanUsageTotals{Tokens: r.Tokens, Value: r.ProviderValue}
}
