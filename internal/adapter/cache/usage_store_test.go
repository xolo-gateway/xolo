package cache

import (
	"context"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// countingUsageStore records how often the budget totals were actually read
// from the backend, which is the whole point of the cache in front of it.
type countingUsageStore struct {
	port.UsageStore

	total int64
	reads int

	recorded []model.UsageRecord
}

func (s *countingUsageStore) SumQuotaCostSince(_ context.Context, _ model.QuotaScope, _ string, _ model.OrgID, _ time.Time) (int64, error) {
	s.reads++
	return s.total, nil
}

func (s *countingUsageStore) RecordUsage(_ context.Context, record model.UsageRecord) error {
	s.recorded = append(s.recorded, record)
	s.total += record.Cost()
	return nil
}

func newPAYGRecord(userID model.UserID, orgID model.OrgID, cost int64) *model.BaseUsageRecord {
	return model.NewUsageRecord(userID, "", orgID, "provider", "llm-model",
		"fast", "", 10, 0, 10, cost, "USD", model.CostSourceComputed, "")
}

func TestUsageStore_CachesBudgetTotals(t *testing.T) {
	ctx := context.Background()
	backend := &countingUsageStore{total: 5_000}
	store := NewUsageStore(backend, NewMemoryCache(64), time.Minute)

	since := model.StartOfDay(time.Now())
	for i := 0; i < 3; i++ {
		total, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, "org-1", "org-1", since)
		if err != nil {
			t.Fatalf("SumQuotaCostSince: %v", err)
		}
		if total != 5_000 {
			t.Fatalf("total = %d, want 5000", total)
		}
	}
	if backend.reads != 1 {
		t.Errorf("backend read %d times, want 1: the total must be reused within the TTL", backend.reads)
	}

	// A different window is a different entry, not a cache hit.
	if _, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, "org-1", "org-1", model.StartOfYear(time.Now())); err != nil {
		t.Fatalf("SumQuotaCostSince (year): %v", err)
	}
	if backend.reads != 2 {
		t.Errorf("backend read %d times, want 2: each window has its own total", backend.reads)
	}
}

// TestUsageStore_RecordUsageUpdatesCachedTotals is what keeps a budget from
// being overshot for the length of a TTL: the spending of a request is applied
// to the totals the next one will read.
func TestUsageStore_RecordUsageUpdatesCachedTotals(t *testing.T) {
	ctx := context.Background()
	backend := &countingUsageStore{total: 1_000}
	store := NewUsageStore(backend, NewMemoryCache(64), time.Minute)

	since := model.StartOfDay(time.Now())
	if _, err := store.SumQuotaCostSince(ctx, model.QuotaScopeUser, "user-1", "org-1", since); err != nil {
		t.Fatalf("SumQuotaCostSince: %v", err)
	}
	if _, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, "org-1", "org-1", since); err != nil {
		t.Fatalf("SumQuotaCostSince (org): %v", err)
	}

	if err := store.RecordUsage(ctx, newPAYGRecord("user-1", "org-1", 250)); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	readsBefore := backend.reads
	userTotal, err := store.SumQuotaCostSince(ctx, model.QuotaScopeUser, "user-1", "org-1", since)
	if err != nil {
		t.Fatalf("SumQuotaCostSince: %v", err)
	}
	if userTotal != 1_250 {
		t.Errorf("user total = %d, want 1250: the record must be applied to the cached total", userTotal)
	}
	orgTotal, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, "org-1", "org-1", since)
	if err != nil {
		t.Fatalf("SumQuotaCostSince (org): %v", err)
	}
	if orgTotal != 1_250 {
		t.Errorf("org total = %d, want 1250", orgTotal)
	}
	if backend.reads != readsBefore {
		t.Errorf("backend read again (%d → %d): the increment must not invalidate the entry", readsBefore, backend.reads)
	}
}

// TestUsageStore_IgnoresNonBudgetRecords mirrors what the counters themselves
// do: subscription-covered and zero-cost usage consumes no monetary budget, so
// it must not move a cached total either.
func TestUsageStore_IgnoresNonBudgetRecords(t *testing.T) {
	ctx := context.Background()
	backend := &countingUsageStore{total: 1_000}
	store := NewUsageStore(backend, NewMemoryCache(64), time.Minute)

	since := model.StartOfDay(time.Now())
	if _, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, "org-1", "org-1", since); err != nil {
		t.Fatalf("SumQuotaCostSince: %v", err)
	}

	planned := newPAYGRecord("user-1", "org-1", 500)
	planned.SetPlanCovered(true)
	if err := store.RecordUsage(ctx, planned); err != nil {
		t.Fatalf("RecordUsage (plan): %v", err)
	}
	if err := store.RecordUsage(ctx, newPAYGRecord("user-1", "org-1", 0)); err != nil {
		t.Fatalf("RecordUsage (free): %v", err)
	}

	total, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, "org-1", "org-1", since)
	if err != nil {
		t.Fatalf("SumQuotaCostSince: %v", err)
	}
	if total != 1_000 {
		t.Errorf("total = %d, want 1000: non-budget usage must not move it", total)
	}
}

// TestUsageStore_ApplicationRecordsSkipUserScope guards the key set: an
// application principal has no user budget, so nothing must be attributed to
// its id.
func TestUsageStore_ApplicationRecordsSkipUserScope(t *testing.T) {
	ctx := context.Background()
	backend := &countingUsageStore{}
	store := NewUsageStore(backend, NewMemoryCache(64), time.Minute)

	record := model.NewUsageRecord("", "app-1", "org-1", "provider", "llm-model",
		"fast", "", 10, 0, 10, 700, "USD", model.CostSourceComputed, "")

	keys := quotaSumCacheKeysFor(record)
	for _, key := range keys {
		if key == quotaSumCacheKey(model.QuotaScopeUser, "app-1", "org-1", model.StartOfDay(record.CreatedAt())) {
			t.Fatalf("an application must not hold a user-scope total, got key %q", key)
		}
	}
	// Day, month and year of the organization only.
	if len(keys) != 3 {
		t.Errorf("keys = %v, want the three organization windows", keys)
	}

	if err := store.RecordUsage(ctx, record); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
}
