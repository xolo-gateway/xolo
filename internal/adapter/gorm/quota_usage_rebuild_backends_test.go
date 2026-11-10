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
		records := []*xologorm.UsageRecord{
			{ID: "u1", CreatedAt: now, UserID: "user-a", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 1_000, Currency: "USD"},
			{ID: "u2", CreatedAt: now, UserID: "user-a", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 2_000, Currency: "USD"},
			{ID: "u3", CreatedAt: now, ApplicationID: "app-1", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 300, Currency: "USD"},
			{ID: "u4", CreatedAt: now, UserID: "user-c", TeamID: "team-1", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 70, Currency: "USD"},
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
	})
}
