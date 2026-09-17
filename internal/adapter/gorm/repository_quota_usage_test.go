package gorm_test

import (
	"context"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TestQuotaUsage_MatchesUsageAggregates pins the running counters against the
// aggregation they replace on the hot path: whatever the enforcer reads from
// quota_usages must be what a SUM over usage_records would have returned.
func TestQuotaUsage_MatchesUsageAggregates(t *testing.T) {
	eachBackend(t, scenarioQuotaUsageMatchesAggregates)
}

func scenarioQuotaUsageMatchesAggregates(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()
	f := seedUsage(t, store)
	since := model.StartOfDay(time.Now())

	// User A's own PAYG spending in their org: 1000 + 2000. The subscription
	// record is excluded, as is the other org's.
	userTotal, err := store.SumQuotaCostSince(ctx, model.QuotaScopeUser, string(f.userA), f.orgID, since)
	if err != nil {
		t.Fatalf("SumQuotaCostSince (user): %v", err)
	}
	if userTotal != 3_000 {
		t.Errorf("user total = %d, want 3000", userTotal)
	}

	legacy, err := store.SumCostSince(ctx, f.userA, f.orgID, since)
	if err != nil {
		t.Fatalf("SumCostSince: %v", err)
	}
	if userTotal != legacy {
		t.Errorf("counter total = %d but aggregation over usage_records = %d: the two must agree", userTotal, legacy)
	}

	// The org total spans every principal, the application record included:
	// 1000 + 2000 + 4000 + 8000.
	orgTotal, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, string(f.orgID), f.orgID, since)
	if err != nil {
		t.Fatalf("SumQuotaCostSince (org): %v", err)
	}
	if orgTotal != 15_000 {
		t.Errorf("org total = %d, want 15000", orgTotal)
	}

	// A window opening after every record sums to nothing, which is what makes
	// a counter roll over at midnight without any invalidation.
	future, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, string(f.orgID), f.orgID, time.Now().AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("SumQuotaCostSince (future): %v", err)
	}
	if future != 0 {
		t.Errorf("future window total = %d, want 0", future)
	}
}

// TestQuotaUsage_AccumulatesAcrossRecords asserts the upsert adds to the day's
// counter instead of replacing it — the failure mode that would silently cap
// every budget at the cost of its last request.
func TestQuotaUsage_AccumulatesAcrossRecords(t *testing.T) {
	eachBackend(t, scenarioQuotaUsageAccumulates)
}

func scenarioQuotaUsageAccumulates(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()
	orgID := model.NewOrgID()
	userID := model.NewUserID()
	providerID := model.NewProviderID()
	modelID := model.NewLLMModelID()
	since := model.StartOfDay(time.Now())

	for i := 0; i < 5; i++ {
		record := model.NewUsageRecord(userID, "", orgID, providerID, modelID,
			"fast", "", 10, 0, 10, 100, "USD", model.CostSourceComputed, "")
		if err := store.RecordUsage(ctx, record); err != nil {
			t.Fatalf("RecordUsage(%d): %v", i, err)
		}
	}

	total, err := store.SumQuotaCostSince(ctx, model.QuotaScopeUser, string(userID), orgID, since)
	if err != nil {
		t.Fatalf("SumQuotaCostSince: %v", err)
	}
	if total != 500 {
		t.Errorf("total = %d, want 500: the counter must add up, not overwrite", total)
	}
}

// TestQuotaUsage_SeparatesScopesAndOrgs guards the counter key: a user's
// budget is per organization, and one organization's spending must never be
// read as another's.
func TestQuotaUsage_SeparatesScopesAndOrgs(t *testing.T) {
	eachBackend(t, scenarioQuotaUsageSeparatesScopes)
}

func scenarioQuotaUsageSeparatesScopes(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()
	f := seedUsage(t, store)
	since := model.StartOfDay(time.Now())

	// User A also spent 9999 in another org; that must not show here.
	other, err := store.SumQuotaCostSince(ctx, model.QuotaScopeUser, string(f.userA), f.otherOrgID, since)
	if err != nil {
		t.Fatalf("SumQuotaCostSince (other org): %v", err)
	}
	if other != 9_999 {
		t.Errorf("other org total = %d, want 9999", other)
	}

	// An application principal feeds the org counter but has no user counter of
	// its own: nothing is stored under its id.
	appTotal, err := store.SumQuotaCostSince(ctx, model.QuotaScopeUser, string(f.appID), f.orgID, since)
	if err != nil {
		t.Fatalf("SumQuotaCostSince (application): %v", err)
	}
	if appTotal != 0 {
		t.Errorf("application user-scope total = %d, want 0", appTotal)
	}
}
