package gorm

import (
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	gormpkg "gorm.io/gorm"
)

// TestBackfillQuotaUsage asserts that an instance upgrading with a populated
// usage history keeps its budget totals: the counters are rebuilt from the
// records rather than starting from zero, which would hand every organization
// a fresh budget on the day of the upgrade.
func TestBackfillQuotaUsage(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	if err := db.AutoMigrate(&UsageRecord{}); err != nil {
		t.Fatalf("migrate usage_records: %v", err)
	}

	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)
	records := []*UsageRecord{
		{ID: "u1", CreatedAt: now, UserID: "user-a", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 1_000, Currency: "USD"},
		{ID: "u2", CreatedAt: now, UserID: "user-a", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 2_000, Currency: "USD"},
		{ID: "u3", CreatedAt: yesterday, UserID: "user-b", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 500, Currency: "USD"},
		// An application principal: it feeds the org counter only.
		{ID: "u4", CreatedAt: now, ApplicationID: "app-1", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 300, Currency: "USD"},
		// Subscription-covered usage consumes no monetary budget.
		{ID: "u5", CreatedAt: now, UserID: "user-a", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 9_999, Currency: "USD", PlanCovered: 1},
		// Another organization, to catch a backfill that mixes scopes.
		{ID: "u6", CreatedAt: now, UserID: "user-a", OrgID: "org-2", ProviderID: "p", ModelID: "m", Cost: 42, Currency: "USD"},
	}
	for _, r := range records {
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("seed %s: %v", r.ID, err)
		}
	}

	if err := migrateQuotaUsageCounters(db); err != nil {
		t.Fatalf("migrateQuotaUsageCounters: %v", err)
	}

	sum := func(scope, scopeID, orgID string, since time.Time) int64 {
		t.Helper()
		var result struct{ Total int64 }
		err := db.Model(&QuotaUsage{}).
			Select("COALESCE(SUM(cost), 0) as total").
			Where("scope = ? AND scope_id = ? AND org_id = ? AND day >= ?", scope, scopeID, orgID, quotaUsageDay(since)).
			Scan(&result).Error
		if err != nil {
			t.Fatalf("sum %s/%s: %v", scope, scopeID, err)
		}
		return result.Total
	}

	if got := sum("user", "user-a", "org-1", startOfDayLocal(now)); got != 3_000 {
		t.Errorf("user-a today = %d, want 3000", got)
	}
	// Yesterday's record is outside today's window but inside a monthly one.
	if got := sum("user", "user-b", "org-1", startOfDayLocal(now)); got != 0 {
		t.Errorf("user-b today = %d, want 0", got)
	}
	if got := sum("user", "user-b", "org-1", startOfDayLocal(yesterday)); got != 500 {
		t.Errorf("user-b since yesterday = %d, want 500", got)
	}
	// 1000 + 2000 + 300 today; the plan-covered record is left out.
	if got := sum("org", "org-1", "org-1", startOfDayLocal(now)); got != 3_300 {
		t.Errorf("org-1 today = %d, want 3300", got)
	}
	if got := sum("user", "app-1", "org-1", startOfDayLocal(now)); got != 0 {
		t.Errorf("application user-scope total = %d, want 0", got)
	}
	if got := sum("org", "org-2", "org-2", startOfDayLocal(now)); got != 42 {
		t.Errorf("org-2 today = %d, want 42", got)
	}

	// Re-running the migration must not double the totals: the backfill is
	// replayed whole on a partially filled table.
	if err := migrateQuotaUsageCounters(db); err != nil {
		t.Fatalf("second migrateQuotaUsageCounters: %v", err)
	}
	if got := sum("org", "org-1", "org-1", startOfDayLocal(now)); got != 3_300 {
		t.Errorf("org-1 after replay = %d, want 3300", got)
	}
}

func startOfDayLocal(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
