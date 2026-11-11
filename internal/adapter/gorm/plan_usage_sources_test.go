package gorm

import (
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// A key window and the window it overrides can start days apart. Each reads
// the records of its own head, part of an hour: one query bounded by both
// heads read every record between them, several days of them on a busy
// plan (#230).
func TestPlanUsageSourcesBoundEachHead(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	now := time.Date(2026, 10, 30, 15, 20, 0, 0, time.Local)
	scope := model.PlanUsageScope{OrgID: "org-1", ProviderID: "p1", KeyWindows: map[model.ProviderKeyID]time.Time{
		"k2": now.Add(-32 * time.Minute),
	}}
	since := now.Add(-10*24*time.Hour - 36*time.Minute)

	heads := 0
	for _, source := range planUsageSources(scope, since, now) {
		statement := source(db.Session(&gormpkg.Session{})).Find(&[]map[string]any{}).Statement
		if statement.Table != "usage_records" {
			continue
		}
		var times []time.Time
		for _, v := range statement.Vars {
			if at, ok := v.(time.Time); ok {
				times = append(times, at)
			}
		}
		if len(times) == 0 {
			t.Fatalf("a records source has no time bound: %s", statement.SQL.String())
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
		if last.Sub(first) > time.Hour {
			t.Errorf("a records query spans %s: %s", last.Sub(first), statement.SQL.String())
		}
		// Both windows start before the current hour: every records query
		// is a head, bounded on both sides.
		if !strings.Contains(statement.SQL.String(), "created_at <") {
			t.Errorf("a records query has no upper bound: %s", statement.SQL.String())
		}
		heads++
	}
	if heads < 2 {
		t.Fatalf("expected a head query per window, got %d", heads)
	}
}

// The current hour is read from its hourly row: its records grow with the
// traffic, and every proxied request runs these reads. Only a window starting
// in the current hour reads records past its head, from its own start.
func TestPlanUsageSourcesReadTheCurrentHourFromItsRow(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Date(2026, 10, 30, 15, 20, 0, 0, time.Local)
	statements := func(scope model.PlanUsageScope, since time.Time) (records []*gormpkg.Statement, hourly []*gormpkg.Statement) {
		for _, source := range planUsageSources(scope, since, now) {
			statement := source(db.Session(&gormpkg.Session{})).Find(&[]map[string]any{}).Statement
			if statement.Table == "usage_records" {
				records = append(records, statement)
			} else {
				hourly = append(hourly, statement)
			}
		}
		return records, hourly
	}

	scope := model.PlanUsageScope{OrgID: "org-1", ProviderID: "p1"}
	records, hourly := statements(scope, now.Add(-5*time.Hour))
	if len(records) != 1 || !strings.Contains(records[0].SQL.String(), "created_at <") {
		t.Fatalf("expected the head alone from the records, got %d queries", len(records))
	}
	if len(hourly) != 1 || strings.Contains(hourly[0].SQL.String(), "hour <") {
		t.Fatalf("expected one hourly query reaching the current hour, got %d", len(hourly))
	}

	// A key window opened 5 minutes ago: its records from then, nothing
	// earlier.
	start := now.Add(-5 * time.Minute)
	scope.KeyWindows = map[model.ProviderKeyID]time.Time{"k2": start}
	records, _ = statements(scope, now.Add(-5*time.Hour))
	tails := 0
	for _, statement := range records {
		if strings.Contains(statement.SQL.String(), "created_at <") {
			continue
		}
		tails++
		for _, v := range statement.Vars {
			if at, ok := v.(time.Time); ok && at.Before(start) {
				t.Errorf("the current hour's records are read from %s, before the window opened: %s", at, statement.SQL.String())
			}
		}
	}
	if tails != 1 {
		t.Fatalf("expected one records query for the window opened in the current hour, got %d", tails)
	}
}
