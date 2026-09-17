package gorm

import (
	"context"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/xolo-gateway/xolo/internal/core/model"
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

// legacyUsageRecord is usage_records as it stood before this change: every
// column of the current entity except status, and no quota_usages table beside
// it.
type legacyUsageRecord struct {
	ID string `gorm:"primaryKey;autoIncrement:false"`
	// Composite indexes cover the time-ranged aggregations (SumCostSince*, chart
	// GROUP BYs) that filter on org/user + created_at, avoiding full table scans.
	//
	// idx_usage_org_payg_cost is a covering index for the PAYG cost sums run by
	// the quota enforcer on every proxy request (org, plan_covered, created_at
	// range, then user_id / currency / cost read from the index itself): on a
	// yearly budget it otherwise visited every row of the org since January.
	//
	// idx_usage_org_prov_plan is its subscription counterpart. It leads on
	// provider_id, which the PAYG index does not carry, and serves the plan-wide
	// reads of the fair-share allocator. The DISTINCT count of active users is
	// answered from the index alone; the window totals use it for the range only
	// and then read total_tokens and provider_cost from the table.
	//
	// idx_usage_org_prov_user answers the per-user presence probe of the same
	// allocator: user_id comes before the created_at range, so the equality on
	// the caller bounds the index and the probe stops at the first entry. On
	// idx_usage_org_prov_plan user_id sits after the range and cannot do that.
	// It does not serve the caller's own totals — user_id sits after the
	// created_at range, so an equality on it cannot be used as a prefix; that
	// query stays on idx_usage_user_org_created.
	CreatedAt         time.Time `gorm:"index:idx_usage_org_created,priority:2;index:idx_usage_user_org_created,priority:3;index:idx_usage_org_payg_cost,priority:3;index:idx_usage_org_prov_plan,priority:4;index:idx_usage_org_prov_user,priority:5"`
	UserID            string    `gorm:"index;index:idx_usage_user_org_created,priority:1;index:idx_usage_org_payg_cost,priority:4;index:idx_usage_org_prov_plan,priority:5;index:idx_usage_org_prov_user,priority:4"`
	ApplicationID     string    `gorm:"index"`
	OrgID             string    `gorm:"index;not null;index:idx_usage_org_created,priority:1;index:idx_usage_user_org_created,priority:2;index:idx_usage_org_payg_cost,priority:1;index:idx_usage_org_prov_plan,priority:1;index:idx_usage_org_prov_user,priority:1"`
	ProviderID        string    `gorm:"index;not null;index:idx_usage_org_prov_plan,priority:2;index:idx_usage_org_prov_user,priority:2"`
	ModelID           string    `gorm:"index;not null"`
	ProxyModelName    string    `gorm:"not null"`
	ResolvedModelName string    `gorm:""`      // actual model used when virtual model was resolved
	AuthTokenID       string    `gorm:"index"` // empty = web session
	PromptTokens      int
	CachedTokens      int
	CompletionTokens  int
	TotalTokens       int
	Cost              int64  `gorm:"index:idx_usage_org_payg_cost,priority:6"` // microcents, frozen at recording time (converted to org currency)
	Currency          string `gorm:"index:idx_usage_org_payg_cost,priority:5"` // frozen from provider
	CostSource        string // "provider" or "computed", see model.CostSource
	PlanCovered       int    `gorm:"index;default:0;index:idx_usage_org_payg_cost,priority:2;index:idx_usage_org_prov_plan,priority:3;index:idx_usage_org_prov_user,priority:3"` // 1 if served by a subscription provider
	ProviderCost      int64  // equivalent PAYG cost in provider currency (microcents), for plan value budgets
}

// TestUpgradeFromExistingDatabase walks the path the two new migrations exist
// for: an instance that already holds usage history. A fresh install goes
// through InitSchema, which creates the tables and marks every migration
// applied without running it, so nothing else covers this.
func TestUpgradeFromExistingDatabase(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	// The pre-change schema, plus the migration ledger of an instance that has
	// applied everything up to the last index migration.
	if err := db.Table("usage_records").AutoMigrate(&legacyUsageRecord{}); err != nil {
		t.Fatalf("migrate legacy usage_records: %v", err)
	}
	if err := db.Exec("CREATE TABLE migrations (id VARCHAR(255) PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create migrations table: %v", err)
	}
	applied := []string{
		"202602010001", "202506040001", "202606080001", "202606180001", "202606250001",
		"202506290001", "202606290001", "202607010001", "202607050001", "202607050002",
		"202607170001", "202607210001", "202607220001", "202608130001", "202608220001",
		"202609040001", "202609150001", "202609150002",
	}
	for _, id := range applied {
		if err := db.Exec("INSERT INTO migrations (id) VALUES (?)", id).Error; err != nil {
			t.Fatalf("mark %s applied: %v", id, err)
		}
	}

	now := time.Now()
	records := []legacyUsageRecord{
		{ID: "u1", CreatedAt: now, UserID: "user-a", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 1_200, Currency: "USD"},
		{ID: "u2", CreatedAt: now, UserID: "user-a", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 800, Currency: "USD"},
		{ID: "u3", CreatedAt: now, ApplicationID: "app-1", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 500, Currency: "USD"},
		{ID: "u4", CreatedAt: now, UserID: "user-a", OrgID: "org-1", ProviderID: "p", ModelID: "m", Cost: 9_999, Currency: "USD", PlanCovered: 1},
	}
	for _, r := range records {
		if err := db.Table("usage_records").Create(&r).Error; err != nil {
			t.Fatalf("seed %s: %v", r.ID, err)
		}
	}

	if _, err := createGetDatabase(db)(context.Background()); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	// 202609170001: the column exists and every pre-existing row reads "ok".
	var statuses []string
	if err := db.Raw("SELECT status FROM usage_records ORDER BY id").Scan(&statuses).Error; err != nil {
		t.Fatalf("read statuses: %v", err)
	}
	if len(statuses) != len(records) {
		t.Fatalf("read %d statuses, want %d", len(statuses), len(records))
	}
	for i, status := range statuses {
		if status != string(model.UsageStatusOK) {
			t.Errorf("record %d: status = %q, want %q", i, status, model.UsageStatusOK)
		}
	}

	// 202609170002: the counters carry the history, so a budget does not reset
	// on the day of the upgrade.
	sum := func(scope, scopeID string) int64 {
		t.Helper()
		var result struct{ Total int64 }
		err := db.Model(&QuotaUsage{}).
			Select("COALESCE(SUM(cost), 0) as total").
			Where("scope = ? AND scope_id = ? AND org_id = ?", scope, scopeID, "org-1").
			Scan(&result).Error
		if err != nil {
			t.Fatalf("sum %s/%s: %v", scope, scopeID, err)
		}
		return result.Total
	}
	if got := sum("user", "user-a"); got != 2_000 {
		t.Errorf("user-a counter = %d, want 2000", got)
	}
	if got := sum("org", "org-1"); got != 2_500 {
		t.Errorf("org-1 counter = %d, want 2500: the application's spending counts too", got)
	}

	// A record written after the upgrade keeps adding to the same counters.
	store := &Store{getDatabase: createGetDatabase(db)}
	record := model.NewUsageRecord("user-a", "", "org-1", "p", "m",
		"fast", "", 10, 0, 10, 300, "USD", model.CostSourceComputed, "")
	if err := store.RecordUsage(context.Background(), record); err != nil {
		t.Fatalf("RecordUsage after upgrade: %v", err)
	}
	if got := sum("user", "user-a"); got != 2_300 {
		t.Errorf("user-a counter after a new record = %d, want 2300", got)
	}
}
