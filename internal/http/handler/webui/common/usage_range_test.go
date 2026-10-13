package common

import (
	"testing"
	"time"
)

func TestUsageRange(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		param, period string
		since         time.Time
	}{
		{"1d", "1d", now.AddDate(0, 0, -1)},
		{"7d", "7d", now.AddDate(0, 0, -7)},
		{"30d", "30d", now.AddDate(0, -1, 0)},
		{"90d", "90d", now.AddDate(0, -3, 0)},
		{"180d", "180d", now.AddDate(0, -6, 0)},
		{"365d", "365d", now.AddDate(-1, 0, 0)},
		// Every other value is the default period, under the same name.
		{"", "7d", now.AddDate(0, 0, -7)},
		{"2d", "7d", now.AddDate(0, 0, -7)},
	} {
		period, since := UsageRange(tc.param, now)
		if period != tc.period || !since.Equal(tc.since) {
			t.Errorf("UsageRange(%q) = %q, %v; want %q, %v", tc.param, period, since, tc.period, tc.since)
		}
	}
}
