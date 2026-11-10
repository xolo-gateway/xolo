package gorm_test

import (
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	gormpkg "gorm.io/gorm"
)

// The rebuild groups by the local day, an expression with a bound parameter.
// PostgreSQL refuses a GROUP BY whose expression is bound to a different
// parameter than the selected one, whatever their values: the migration and
// the seeder failed there while SQLite accepted them.
func TestRebuildQuotaUsageOnEachBackend(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		if err := db.AutoMigrate(&xologorm.UsageRecord{}, &xologorm.QuotaUsage{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}

		now := time.Now()
		year, month, day := now.Date()
		midnight := time.Date(year, month, day, 0, 0, 0, 0, time.Local)
		records := []*xologorm.UsageRecord{
			{ID: "u1", CreatedAt: now, UserID: "user-a", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 1_000, Currency: "USD"},
			{ID: "u2", CreatedAt: now, UserID: "user-a", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 2_000, Currency: "USD"},
			{ID: "u3", CreatedAt: now, ApplicationID: "app-1", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 300, Currency: "USD"},
			{ID: "u4", CreatedAt: now, UserID: "user-c", TeamID: "team-1", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 70, Currency: "USD"},
			// Either side of local midnight, far enough from it that a DST
			// shift of the current offset cannot move them into the same day:
			// the rebuild must bucket them in the server's local days.
			{ID: "u5", CreatedAt: midnight.Add(-6 * time.Hour), UserID: "user-d", OrgID: "org-2", ProviderID: "p", ModelID: "m", Cost: 5, Currency: "USD"},
			{ID: "u6", CreatedAt: midnight.Add(6 * time.Hour), UserID: "user-d", OrgID: "org-2", ProviderID: "p", ModelID: "m", Cost: 7, Currency: "USD"},
		}
		for _, r := range records {
			if err := db.Create(r).Error; err != nil {
				t.Fatalf("seed %s: %v", r.ID, err)
			}
		}

		if err := xologorm.RebuildQuotaUsage(db); err != nil {
			t.Fatalf("RebuildQuotaUsage: %v", err)
		}

		sum := func(scope, scopeID string) int64 {
			t.Helper()
			var result struct{ Total int64 }
			if err := db.Model(&xologorm.QuotaUsage{}).
				Select("COALESCE(SUM(cost), 0) as total").
				Where("scope = ? AND scope_id = ?", scope, scopeID).
				Scan(&result).Error; err != nil {
				t.Fatalf("sum %s/%s: %v", scope, scopeID, err)
			}
			return result.Total
		}

		for _, tc := range []struct {
			scope, scopeID string
			want           int64
		}{
			{"org", "org-1", 3_370},
			{"user", "user-a", 3_000},
			{"application", "app-1", 300},
			{"team", "team-1", 70},
			{"team_member", "team-1:user-c", 70},
		} {
			if got := sum(tc.scope, tc.scopeID); got != tc.want {
				t.Errorf("%s %s: got %d, want %d", tc.scope, tc.scopeID, got, tc.want)
			}
		}

		var days []struct {
			Day  string
			Cost int64
		}
		if err := db.Model(&xologorm.QuotaUsage{}).Select("day, cost").
			Where("scope = ? AND scope_id = ?", "user", "user-d").Order("day").Scan(&days).Error; err != nil {
			t.Fatalf("days: %v", err)
		}
		// The rebuild shifts by the server's current offset, not by the offset
		// each timestamp had: expect the same, or a DST transition day fails.
		_, offset := time.Now().Zone()
		bucket := func(at time.Time) string {
			return at.UTC().Add(time.Duration(offset) * time.Second).Format("2006-01-02")
		}
		before := bucket(midnight.Add(-6 * time.Hour))
		after := bucket(midnight.Add(6 * time.Hour))
		if len(days) != 2 || days[0].Day != before || days[0].Cost != 5 || days[1].Day != after || days[1].Cost != 7 {
			t.Fatalf("expected %s=5 and %s=7, got %+v", before, after, days)
		}
	})
}
