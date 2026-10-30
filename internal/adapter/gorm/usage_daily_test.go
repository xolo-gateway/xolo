package gorm_test

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/rs/xid"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	gormpkg "gorm.io/gorm"
)

// pastUsage writes a usage record at a given time, as the history the daily
// sums are rebuilt from. The store only writes records at the current time.
func pastUsage(t *testing.T, db *gormpkg.DB, at time.Time, userID, orgID, model string, cost int64, planCovered bool) {
	t.Helper()
	covered := 0
	if planCovered {
		covered = 1
	}
	rec := &xologorm.UsageRecord{
		ID: xid.New().String(), CreatedAt: at, UserID: userID, OrgID: orgID, ProviderID: "p1", ModelID: "m-" + model,
		ProxyModelName: model, PromptTokens: 10, CachedTokens: 2, CompletionTokens: 20, TotalTokens: 30,
		Cost: cost, Currency: "EUR", PlanCovered: covered, Status: "ok",
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create past usage: %v", err)
	}
}

// localNoon is noon, server time, days before today. Records stay far from
// midnight so their day does not depend on the database's timezone.
func localNoon(days int) time.Time {
	y, m, d := time.Now().Date()
	return time.Date(y, m, d-days, 12, 0, 0, 0, time.Local)
}

// The usage pages read the whole days of their period from usage_daily and
// the rest from usage_records: the result must be the one the records alone
// give.
func TestUsageDaily_PeriodReadsMatchTheRecords(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := context.Background()
		store := newStoreOn(t, db)
		migrateSchema(t, store)

		// The first day of a 7-day period starting at 15:00: one record before
		// the start, one after.
		since := localNoon(7).Add(3 * time.Hour)
		pastUsage(t, db, localNoon(7).Add(-2*time.Hour), "u1", "org-1", "gpt", 1, false)
		pastUsage(t, db, localNoon(7).Add(4*time.Hour), "u1", "org-1", "gpt", 10, false)
		// Whole days.
		pastUsage(t, db, localNoon(6), "u1", "org-1", "gpt", 100, false)
		pastUsage(t, db, localNoon(6), "u1", "org-1", "gpt", 200, false)
		pastUsage(t, db, localNoon(4), "u1", "org-1", "claude", 300, true)
		pastUsage(t, db, localNoon(4), "u2", "org-1", "claude", 400, false)
		pastUsage(t, db, localNoon(2), "u2", "org-2", "gpt", 500, false)
		pastUsage(t, db, localNoon(1).Add(8*time.Hour), "u1", "org-1", "gpt", 600, false)
		// Before the period.
		pastUsage(t, db, localNoon(9), "u1", "org-1", "gpt", 7000, false)
		if err := xologorm.RebuildUsageDaily(db); err != nil {
			t.Fatalf("RebuildUsageDaily: %v", err)
		}
		// Today, through the store.
		recordUsage(t, store, "u1", "p1", "m-gpt", "gpt", "", "EUR", 1000, 10, 20, false)

		org1, user1, payg := model.OrgID("org-1"), model.UserID("u1"), false
		filters := map[string]port.UsageFilter{
			"all":          {Since: &since},
			"organization": {OrgID: &org1, Since: &since},
			"user":         {UserID: &user1, Since: &since},
			"payg":         {OrgID: &org1, PlanCovered: &payg, Since: &since},
		}
		dimensions := []port.UsageDimension{port.UsageDimensionModel, port.UsageDimensionDay, port.UsageDimensionUser, port.UsageDimensionProvider}
		for name, filter := range filters {
			t.Run(name, func(t *testing.T) {
				// An end of period makes the store read the records alone.
				recordsOnly := filter
				until := time.Now().Add(time.Hour)
				recordsOnly.Until = &until

				got, err := store.AggregateUsage(ctx, filter)
				if err != nil {
					t.Fatalf("AggregateUsage: %v", err)
				}
				want, err := store.AggregateUsage(ctx, recordsOnly)
				if err != nil {
					t.Fatalf("AggregateUsage over the records: %v", err)
				}
				if *got != *want {
					t.Errorf("AggregateUsage = %+v, the records give %+v", *got, *want)
				}

				gotCosts, err := store.AggregateCostByDimensions(ctx, filter, dimensions...)
				if err != nil {
					t.Fatalf("AggregateCostByDimensions: %v", err)
				}
				wantCosts, err := store.AggregateCostByDimensions(ctx, recordsOnly, dimensions...)
				if err != nil {
					t.Fatalf("AggregateCostByDimensions over the records: %v", err)
				}
				for _, d := range dimensions {
					if g, w := sortedCosts(gotCosts[d]), sortedCosts(wantCosts[d]); !slices.Equal(g, w) {
						t.Errorf("%s: got %v, the records give %v", d, g, w)
					}
				}
			})
		}

		// The expected totals, so the comparison above is not between two
		// equally wrong reads.
		agg, err := store.AggregateUsage(ctx, filters["organization"])
		if err != nil {
			t.Fatal(err)
		}
		if agg.TotalRequests != 7 || agg.TotalCost != 10+100+200+300+400+600+1000 {
			t.Errorf("organization: %d requests for %d, want 7 for 2610", agg.TotalRequests, agg.TotalCost)
		}
	})
}

// A record written through the store adds to its day the sums a rebuild from
// the records gives.
func TestUsageDaily_RecordingMatchesARebuild(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newStoreOn(t, db)
		recordUsage(t, store, "u1", "p1", "m-gpt", "gpt", "", "EUR", 1000, 10, 20, false)
		recordUsage(t, store, "u1", "p1", "m-gpt", "gpt", "", "EUR", 500, 1, 2, false)
		recordUsage(t, store, "u1", "p1", "m-gpt", "virtual", "gpt", "EUR", 70, 1, 2, false)
		recordUsage(t, store, "u2", "p1", "m-claude", "claude", "", "USD", 30, 3, 4, true)

		// The virtual model resolved to gpt is charted as gpt: it shares its row.
		recorded := dailyRows(t, db)
		if len(recorded) != 2 {
			t.Fatalf("expected 2 daily rows, got %d: %v", len(recorded), recorded)
		}
		if recorded[0].ModelName != "gpt" || recorded[0].Requests != 3 || recorded[0].Cost != 1570 {
			t.Errorf("gpt row: got %+v, want 3 requests for 1570", recorded[0])
		}
		if err := xologorm.RebuildUsageDaily(db); err != nil {
			t.Fatalf("RebuildUsageDaily: %v", err)
		}
		if rebuilt := dailyRows(t, db); !slices.Equal(recorded, rebuilt) {
			t.Errorf("recorded %v, rebuilt %v", recorded, rebuilt)
		}
	})
}

func dailyRows(t *testing.T, db *gormpkg.DB) []xologorm.UsageDaily {
	t.Helper()
	var rows []xologorm.UsageDaily
	if err := db.Order("org_id, day, user_id, model_name, currency, plan_covered").Find(&rows).Error; err != nil {
		t.Fatalf("read usage_daily: %v", err)
	}
	return rows
}

func sortedCosts(rows []port.DimensionCost) []port.DimensionCost {
	return slices.SortedFunc(slices.Values(rows), func(a, b port.DimensionCost) int {
		return cmp.Or(
			cmp.Compare(a.Key, b.Key),
			cmp.Compare(a.OrgID, b.OrgID),
			cmp.Compare(a.Currency, b.Currency),
			cmp.Compare(fmt.Sprint(a.PlanCovered), fmt.Sprint(b.PlanCovered)),
		)
	})
}
