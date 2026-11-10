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
		// A head is bounded on both sides; the current hour only from below.
		if strings.Contains(statement.SQL.String(), "created_at <") {
			heads++
		}
	}
	if heads < 2 {
		t.Fatalf("expected a head query per window, got %d", heads)
	}
}
