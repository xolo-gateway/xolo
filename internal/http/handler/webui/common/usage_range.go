package common

import "time"

// UsageRange resolves the range parameter of the usage pages. It returns the
// period the parameter names, unknown values falling back to 7 days, and the
// start of that period. Caches key on the period, never on its start: the
// start moves with the clock.
func UsageRange(r string, now time.Time) (period string, since time.Time) {
	switch r {
	case "1d":
		return r, now.AddDate(0, 0, -1)
	case "30d":
		return r, now.AddDate(0, -1, 0)
	case "90d":
		return r, now.AddDate(0, -3, 0)
	case "180d":
		return r, now.AddDate(0, -6, 0)
	case "365d":
		return r, now.AddDate(-1, 0, 0)
	default:
		return "7d", now.AddDate(0, 0, -7)
	}
}
