package gorm_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// The counters kept as usage is recorded, concurrently and on both backends,
// must equal the ones rebuilt from the records: on PostgreSQL they are written
// by a single statement chaining every upsert (issue #223).
func TestRecordedCountersMatchTheHistoryOnEachBackend(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newSeededStore(t, db)
		ctx := context.Background()
		org := model.NewOrgID()
		team := model.TeamID(model.NewOrgID())

		records := func(i int) model.UsageRecord {
			user := model.UserID([]string{"user-a", "user-b", "user-c"}[i%3])
			switch i % 4 {
			case 0: // a user
				return model.NewUsageRecord(user, "", org, "", "prov", "mdl", "fast", "", 10, 0, 10, 100, "USD", model.CostSourceComputed, "")
			case 1: // an application
				return model.NewUsageRecord("", "app-1", org, "", "prov", "mdl", "fast", "", 10, 0, 10, 70, "USD", model.CostSourceComputed, "")
			case 2: // a team member
				return model.NewUsageRecord(user, "", org, team, "prov", "mdl", "fast", "", 10, 0, 10, 30, "USD", model.CostSourceComputed, "")
			default: // covered by a plan
				r := model.NewUsageRecord(user, "", org, "", "prov", "mdl", "fast", "", 10, 0, 10, 0, "USD", model.CostSourceComputed, "")
				r.SetPlanCovered(true)
				return r
			}
		}

		const writers, perWriter = 8, 25
		var wg sync.WaitGroup
		errs := make(chan error, writers)
		for w := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range perWriter {
					if err := store.RecordUsage(ctx, records(w*perWriter+i)); err != nil {
						errs <- err
						return
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("RecordUsage: %v", err)
		}

		type sums struct{ quota, daily, hourly string }
		snapshot := func() sums {
			t.Helper()
			read := func(query string) string {
				var rows []map[string]any
				if err := db.Raw(query).Scan(&rows).Error; err != nil {
					t.Fatalf("%s: %v", query, err)
				}
				return fmtRows(rows)
			}
			return sums{
				quota:  read("SELECT scope, scope_id, day, cost FROM quota_usages ORDER BY scope, scope_id, day"),
				daily:  read("SELECT user_id, application_id, team_id, plan_covered, requests, cost, total_tokens FROM usage_daily ORDER BY user_id, application_id, team_id, plan_covered"),
				hourly: read("SELECT user_id, requests, total_tokens FROM plan_usage_hourly ORDER BY user_id"),
			}
		}

		recorded := snapshot()
		if recorded.quota == "" || recorded.daily == "" || recorded.hourly == "" {
			t.Fatalf("expected every counter family to be written, got %+v", recorded)
		}
		for _, rebuild := range []func(*gormpkg.DB) error{xologorm.RebuildQuotaUsage, xologorm.RebuildUsageDaily, xologorm.RebuildPlanUsage} {
			if err := rebuild(db); err != nil {
				t.Fatalf("rebuild: %v", err)
			}
		}
		if rebuilt := snapshot(); rebuilt != recorded {
			t.Fatalf("recorded counters differ from the rebuilt ones\nrecorded: %+v\nrebuilt:  %+v", recorded, rebuilt)
		}
	})
}

// fmtRows renders rows read as maps, column order sorted, for comparison.
func fmtRows(rows []map[string]any) string {
	var b strings.Builder
	for _, row := range rows {
		keys := make([]string, 0, len(row))
		for k := range row {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s=%v ", k, row[k])
		}
		b.WriteString("\n")
	}
	return b.String()
}
