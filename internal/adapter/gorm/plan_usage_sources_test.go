package gorm

import (
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// renderedSource is a plan usage source rendered without running it.
type renderedSource struct {
	table string
	sql   string
	times []time.Time
	// periods are the minute or hour strings it is bounded by.
	periods []string
}

func renderPlanUsageSources(t *testing.T, scope model.PlanUsageScope, since, now time.Time) []renderedSource {
	t.Helper()
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var out []renderedSource
	for _, source := range planUsageSources(scope, since, now) {
		statement := source(db.Session(&gormpkg.Session{})).Find(&[]map[string]any{}).Statement
		r := renderedSource{table: statement.Table, sql: statement.SQL.String()}
		for _, v := range statement.Vars {
			switch v := v.(type) {
			case time.Time:
				r.times = append(r.times, v)
			case string:
				if len(v) == len(usageMinuteLayout) || len(v) == len(usageHourLayout) {
					if _, err := time.Parse(usageMinuteLayout, v); err == nil {
						r.periods = append(r.periods, v)
					} else if _, err := time.Parse(usageHourLayout, v); err == nil {
						r.periods = append(r.periods, v)
					}
				}
			}
		}
		out = append(out, r)
	}
	return out
}

func span(times []time.Time) time.Duration {
	if len(times) == 0 {
		return 0
	}
	first, last := times[0], times[0]
	for _, at := range times[1:] {
		if at.Before(first) {
			first = at
		}
		if at.After(last) {
			last = at
		}
	}
	return last.Sub(first)
}

// No plan check reads more than a minute of records, whatever its windows:
// the records of a minute are all a busy plan can afford on every request
// (#232, #234). A key window and the window it overrides can start days
// apart, and each reads its own minutes, bounded by its own times: one query
// bounded by both would read everything between them (#230).
func TestPlanUsageSourcesReadAMinuteOfRecordsAtMost(t *testing.T) {
	now := time.Date(2026, 10, 30, 15, 20, 30, 0, time.Local)
	scope := model.PlanUsageScope{OrgID: "org-1", ProviderID: "p1", KeyWindows: map[model.ProviderKeyID]time.Time{
		"k2": now.Add(-32*time.Minute - 7*time.Second),
	}}
	since := now.Add(-10*24*time.Hour - 36*time.Minute - 12*time.Second)

	var records, minutes int
	for _, source := range renderPlanUsageSources(t, scope, since, now) {
		switch source.table {
		case "usage_records":
			records++
			if len(source.times) == 0 || !strings.Contains(source.sql, "created_at <") {
				t.Errorf("a records query is not bounded on both sides: %s", source.sql)
			}
			if span(source.times) > time.Minute {
				t.Errorf("a records query spans %s: %s", span(source.times), source.sql)
			}
		case "plan_usage_minutely":
			minutes++
			if !strings.Contains(source.sql, "minute <") {
				t.Errorf("both windows start before the current hour, yet a minutes query has no end: %s", source.sql)
			}
			var bounds []time.Time
			for _, p := range source.periods {
				at, _ := time.Parse(usageMinuteLayout, p)
				bounds = append(bounds, at)
			}
			if span(bounds) > time.Hour {
				t.Errorf("a minutes query spans %s: %s", span(bounds), source.sql)
			}
		}
	}
	if records != 2 || minutes != 2 {
		t.Fatalf("expected the records and the minutes of each window apart, got %d and %d queries", records, minutes)
	}
}

// The current hour comes from the sums: a window started hours ago reads its
// first minutes and hours there, and a window opened in the current hour its
// minutes from its own start, never the records of the hour.
func TestPlanUsageSourcesReadTheCurrentHourFromTheSums(t *testing.T) {
	now := time.Date(2026, 10, 30, 15, 20, 0, 0, time.Local)
	tables := func(sources []renderedSource) map[string][]renderedSource {
		out := map[string][]renderedSource{}
		for _, s := range sources {
			out[s.table] = append(out[s.table], s)
		}
		return out
	}

	// On the minute: no records at all.
	scope := model.PlanUsageScope{OrgID: "org-1", ProviderID: "p1"}
	got := tables(renderPlanUsageSources(t, scope, now.Add(-5*time.Hour-20*time.Minute), now))
	if len(got["usage_records"]) != 0 {
		t.Fatalf("a window starting on the minute read records: %s", got["usage_records"][0].sql)
	}
	if len(got["plan_usage_hourly"]) != 1 || strings.Contains(got["plan_usage_hourly"][0].sql, "hour <") {
		t.Fatalf("expected one hourly query reaching the current hour, got %v", got["plan_usage_hourly"])
	}

	// A key window opened 5 minutes and 10 seconds ago: its records up to
	// the next minute, its minutes from there with no end, no hour.
	start := now.Add(-5*time.Minute - 10*time.Second)
	scope.KeyWindows = map[model.ProviderKeyID]time.Time{"k2": start}
	got = tables(renderPlanUsageSources(t, scope, now.Add(-5*time.Hour-20*time.Minute), now))
	if len(got["usage_records"]) != 1 || span(got["usage_records"][0].times) > time.Minute {
		t.Fatalf("expected the window's first seconds alone from the records, got %v", got["usage_records"])
	}
	for _, at := range got["usage_records"][0].times {
		if at.Before(start) {
			t.Errorf("records read from %s, before the window opened", at)
		}
	}
	open := 0
	for _, s := range got["plan_usage_minutely"] {
		if !strings.Contains(s.sql, "minute <") {
			open++
			if len(s.periods) != 1 || s.periods[0] != usageMinute(start.Add(10*time.Second)) {
				t.Errorf("the window opened in the current hour reads its minutes from %v: %s", s.periods, s.sql)
			}
		}
	}
	if open != 1 {
		t.Fatalf("expected one open-ended minutes query, for the window opened in the current hour, got %d", open)
	}
}
