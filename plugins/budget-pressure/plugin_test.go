package main

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

func pressureOf(t *testing.T, q *proto.QuotaInfo) map[string]any {
	t.Helper()
	out, err := (&Plugin{}).PreRequest(context.Background(), &proto.PreRequestInput{Quota: q})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out.OutputsJson), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPreRequest_NoQuota(t *testing.T) {
	m := pressureOf(t, nil)
	if m["budget_pressure"] != 0.0 || m["has_budget"] != false {
		t.Errorf("no quota must mean no pressure: %v", m)
	}
}

func TestPreRequest_WorstPeriodWins(t *testing.T) {
	m := pressureOf(t, &proto.QuotaInfo{
		DailyTotal: 100, DailyRemaining: 90, // 0.1
		MonthlyTotal: 1000, MonthlyRemaining: 250, // 0.75
	})
	if !near(m["daily_pressure"].(float64), 0.1) || !near(m["monthly_pressure"].(float64), 0.75) {
		t.Errorf("unexpected per-period pressure: %v", m)
	}
	if m["yearly_pressure"] != 0.0 {
		t.Errorf("unlimited period must exert no pressure: %v", m["yearly_pressure"])
	}
	if !near(m["budget_pressure"].(float64), 0.75) || m["has_budget"] != true {
		t.Errorf("expected max pressure 0.75: %v", m)
	}
}

func TestPressure_Clamped(t *testing.T) {
	if pressure(100, -50) != 1 {
		t.Error("overspent budget must clamp to 1")
	}
	if pressure(100, 150) != 0 {
		t.Error("remaining above total must clamp to 0")
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
