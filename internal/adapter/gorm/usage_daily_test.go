package gorm_test

import (
	"cmp"
	"context"
	"fmt"
	"maps"
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
	pastTeamUsage(t, db, at, userID, orgID, "", model, cost, planCovered)
}

func pastTeamUsage(t *testing.T, db *gormpkg.DB, at time.Time, userID, orgID, teamID, model string, cost int64, planCovered bool) {
	t.Helper()
	covered := 0
	if planCovered {
		covered = 1
	}
	rec := &xologorm.UsageRecord{
		ID: xid.New().String(), CreatedAt: at, UserID: userID, OrgID: orgID, TeamID: teamID, ProviderID: "p1", ModelID: "m-" + model,
		ProxyModelName: model, PromptTokens: 10, CachedTokens: 2, CompletionTokens: 20, TotalTokens: 30,
		Cost: cost, Currency: "EUR", PlanCovered: covered, Status: "ok",
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create past usage: %v", err)
	}
}

// localNoon is noon, server time, days before today.
func localNoon(days int) time.Time {
	y, m, d := time.Now().Date()
	return time.Date(y, m, d-days, 12, 0, 0, 0, time.Local)
}

// localMidnight is the server-local midnight starting the day days before today.
func localMidnight(days int) time.Time {
	y, m, d := time.Now().Date()
	return time.Date(y, m, d-days, 0, 0, 0, 0, time.Local)
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
		pastTeamUsage(t, db, localNoon(3), "u2", "org-1", "team-1", "gpt", 800, false)
		// Minutes from midnight on the days bounding the period: the day of each
		// must be the server's, whatever the database's timezone.
		pastUsage(t, db, localMidnight(6).Add(5*time.Minute), "u1", "org-1", "gpt", 20000, false)
		pastUsage(t, db, localMidnight(6).Add(-5*time.Minute), "u1", "org-1", "gpt", 40000, false)
		pastUsage(t, db, localMidnight(0).Add(-5*time.Minute), "u1", "org-1", "gpt", 80000, false)
		// Covered by a plan, on the partial first day.
		pastUsage(t, db, localNoon(7).Add(5*time.Hour), "u2", "org-1", "claude", 3, true)
		// Before the period.
		pastUsage(t, db, localNoon(9), "u1", "org-1", "gpt", 7000, false)
		if err := xologorm.RebuildUsageDaily(db); err != nil {
			t.Fatalf("RebuildUsageDaily: %v", err)
		}
		// Today, through the store, one of them covered by a plan.
		recordUsage(t, store, "u1", "p1", "m-gpt", "gpt", "", "EUR", 1000, 10, 20, false)
		recordUsage(t, store, "u2", "p1", "m-gpt", "gpt", "", "EUR", 5, 100, 200, true)

		org1, user1, payg := model.OrgID("org-1"), model.UserID("u1"), false
		team1, provider1, gpt, eur := model.TeamID("team-1"), model.ProviderID("p1"), model.LLMModelID("m-gpt"), "EUR"
		midnight := localMidnight(6)
		filters := map[string]port.UsageFilter{
			"all":           {Since: &since},
			"organization":  {OrgID: &org1, Since: &since},
			"user":          {UserID: &user1, Since: &since},
			"payg":          {OrgID: &org1, PlanCovered: &payg, Since: &since},
			"team":          {OrgID: &org1, TeamID: &team1, Since: &since},
			"provider":      {ProviderID: &provider1, Since: &since},
			"model":         {ModelID: &gpt, Since: &since},
			"currency":      {Currency: &eur, Since: &since},
			"from midnight": {OrgID: &org1, Since: &midnight},
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
					// Read alone, the records are charted on the database's day,
					// which is the server's on SQLite only. The days of the split
					// read are checked below on both.
					if d == port.UsageDimensionDay && db.Dialector.Name() != "sqlite" {
						continue
					}
					if g, w := sortedCosts(gotCosts[d]), sortedCosts(wantCosts[d]); !slices.Equal(g, w) {
						t.Errorf("%s: got %v, the records give %v", d, g, w)
					}
				}
			})
		}

		// The quota and leaderboard sums read the same split.
		t.Run("cost by currency", func(t *testing.T) {
			// PAYG only: the covered records are left out.
			want := map[string]int64{"EUR": 10 + 100 + 200 + 400 + 600 + 800 + 20000 + 40000 + 80000 + 1000}
			for _, users := range [][]model.UserID{{"u1", "u2"}, nil} {
				got, err := store.SumCostSinceByCurrency(ctx, users, org1, since)
				if err != nil {
					t.Fatalf("SumCostSinceByCurrency: %v", err)
				}
				if !maps.Equal(got, want) {
					t.Errorf("users %v: got %v, want %v", users, got, want)
				}
			}
			got, err := store.SumCostSinceByCurrency(ctx, []model.UserID{"u2"}, org1, since)
			if err != nil {
				t.Fatalf("SumCostSinceByCurrency: %v", err)
			}
			if want := map[string]int64{"EUR": 400 + 800}; !maps.Equal(got, want) {
				t.Errorf("u2: got %v, want %v", got, want)
			}
		})
		t.Run("plan tokens by user", func(t *testing.T) {
			until := time.Now().Add(time.Hour)
			recordsOnly := port.UsageFilter{OrgID: &org1, Since: &since, Until: &until}
			split := port.UsageFilter{OrgID: &org1, Since: &since}
			tokens := func(f port.UsageFilter) map[model.UserID]int64 {
				rows, err := store.AggregatePlanTokensByUser(ctx, f)
				if err != nil {
					t.Fatalf("AggregatePlanTokensByUser: %v", err)
				}
				out := map[model.UserID]int64{}
				for _, r := range rows {
					out[r.UserID] += r.Tokens
				}
				return out
			}
			// u1 on a whole day; u2 on the partial first day and today.
			got, want := tokens(split), tokens(recordsOnly)
			if !maps.Equal(got, want) || got["u1"] != 30 || got["u2"] != 30+300 {
				t.Errorf("got %v, the records give %v, want u1: 30, u2: 330", got, want)
			}
		})

		// The server's day of each record, whatever the database's timezone:
		// the minutes before midnight belong to the day before.
		days, err := store.AggregateCostByDimensions(ctx, filters["organization"], port.UsageDimensionDay)
		if err != nil {
			t.Fatal(err)
		}
		gotDays := map[string]int64{}
		for _, row := range days[port.UsageDimensionDay] {
			gotDays[row.Key] += row.Cost
		}
		day := func(days int) string { return localNoon(days).Format("2006-01-02") }
		wantDays := map[string]int64{
			day(7): 10 + 3 + 40000, day(6): 100 + 200 + 20000, day(4): 300 + 400, day(3): 800,
			day(1): 600 + 80000, day(0): 1000 + 5,
		}
		if !maps.Equal(gotDays, wantDays) {
			t.Errorf("organization by day: got %v, want %v", gotDays, wantDays)
		}

		// The expected totals, so the comparison above is not between two
		// equally wrong reads.
		agg, err := store.AggregateUsage(ctx, filters["organization"])
		if err != nil {
			t.Fatal(err)
		}
		if want := int64(10 + 3 + 100 + 200 + 300 + 400 + 600 + 800 + 20000 + 40000 + 80000 + 1000 + 5); agg.TotalRequests != 13 || agg.TotalCost != want {
			t.Errorf("organization: %d requests for %d, want 13 for %d", agg.TotalRequests, agg.TotalCost, want)
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
