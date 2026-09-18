package cache

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// countingUsageStore records how often the budget totals were actually read
// from the backend, which is the whole point of the cache in front of it.
type countingUsageStore struct {
	port.UsageStore

	// The concurrency tests below call both methods at once, so the fake has to
	// be safe itself; without this the race detector reports the fake, not the
	// decorator under test.
	mu    sync.Mutex
	total int64
	reads int

	recorded []model.UsageRecord
}

func (s *countingUsageStore) SumQuotaCostSince(_ context.Context, _ model.QuotaScope, _ string, _ model.OrgID, _ time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.reads++
	return s.total, nil
}

func (s *countingUsageStore) RecordUsage(_ context.Context, record model.UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.recorded = append(s.recorded, record)
	s.total += record.Cost()
	return nil
}

func (s *countingUsageStore) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.reads
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
	if backend.readCount() != 1 {
		t.Errorf("backend read %d times, want 1: the total must be reused within the TTL", backend.readCount())
	}

	// A different window is a different entry, not a cache hit.
	if _, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, "org-1", "org-1", model.StartOfYear(time.Now())); err != nil {
		t.Fatalf("SumQuotaCostSince (year): %v", err)
	}
	if backend.readCount() != 2 {
		t.Errorf("backend read %d times, want 2: each window has its own total", backend.readCount())
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

	readsBefore := backend.readCount()
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
	if backend.readCount() != readsBefore {
		t.Errorf("backend read again (%d → %d): the increment must not invalidate the entry", readsBefore, backend.readCount())
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

// TestUsageStore_KeysFollowThePrincipalOnTheRecord pins which totals a record
// feeds, for the two shapes a record actually takes.
//
// An application authenticates through a shadow user, so in production its
// records carry that user's id and get a user-scope total like anyone else —
// the same id the enforcer looks the budget up under. A record with no
// principal at all feeds the organization only.
func TestUsageStore_KeysFollowThePrincipalOnTheRecord(t *testing.T) {
	ctx := context.Background()
	backend := &countingUsageStore{}
	store := NewUsageStore(backend, NewMemoryCache(64), time.Minute)

	// An application call as the auth extractor produces it: the shadow user's
	// id in UserID, the application's own id alongside.
	shadow := model.NewUsageRecord("usr-shadow-app-1", "app-1", "org-1", "provider", "llm-model",
		"fast", "", 10, 0, 10, 700, "USD", model.CostSourceComputed, "")

	day := model.StartOfDay(shadow.CreatedAt())
	keys := quotaSumCacheKeysFor(shadow)
	if len(keys) != 6 {
		t.Errorf("keys = %v, want three organization windows and three user windows", keys)
	}
	wantUserKey := quotaSumCacheKey(model.QuotaScopeUser, "usr-shadow-app-1", "org-1", day)
	if !slices.Contains(keys, wantUserKey) {
		t.Errorf("keys = %v, want one under the shadow user %q: that is the id the enforcer checks", keys, wantUserKey)
	}
	// Nothing is ever attributed to the application id itself: no budget is
	// resolved under it.
	if slices.Contains(keys, quotaSumCacheKey(model.QuotaScopeUser, "app-1", "org-1", day)) {
		t.Errorf("keys = %v, want no total under the application id", keys)
	}

	if err := store.RecordUsage(ctx, shadow); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	// A record with no principal at all, which the seeded fixture produces,
	// feeds the organization windows only.
	orphan := model.NewUsageRecord("", "app-1", "org-1", "provider", "llm-model",
		"fast", "", 10, 0, 10, 700, "USD", model.CostSourceComputed, "")
	if keys := quotaSumCacheKeysFor(orphan); len(keys) != 3 {
		t.Errorf("keys = %v, want the three organization windows only", keys)
	}
}

// blockingUsageStore holds SumQuotaCostSince open until the test releases it,
// so the window between reading a total and storing it can be driven by hand.
type blockingUsageStore struct {
	port.UsageStore

	total int64

	entered  chan struct{}
	release  chan struct{}
	blockOne bool
}

func (s *blockingUsageStore) SumQuotaCostSince(_ context.Context, _ model.QuotaScope, _ string, _ model.OrgID, _ time.Time) (int64, error) {
	if s.blockOne {
		s.blockOne = false
		// The answer is fixed before the caller is released, the way a database
		// read that started earlier does not see a later commit.
		answer := s.total
		s.entered <- struct{}{}
		<-s.release
		return answer, nil
	}
	return s.total, nil
}

func (s *blockingUsageStore) RecordUsage(_ context.Context, record model.UsageRecord) error {
	s.total += record.Cost()
	return nil
}

// TestUsageStore_DropsTotalReadBeforeAConcurrentRecord pins the guard the whole
// mechanism exists for. A total read before a record was written must not be
// stored after that record's increment ran: the increment finds no entry to
// apply itself to, so storing the total would hide the record's cost until the
// TTL elapsed, and a budget could be overshot.
func TestUsageStore_DropsTotalReadBeforeAConcurrentRecord(t *testing.T) {
	ctx := context.Background()
	backend := &blockingUsageStore{
		total:    1_000,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		blockOne: true,
	}
	store := NewUsageStore(backend, NewMemoryCache(64), time.Minute)
	since := model.StartOfDay(time.Now())

	read := make(chan int64)
	go func() {
		total, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, "org-1", "org-1", since)
		if err != nil {
			t.Errorf("SumQuotaCostSince: %v", err)
		}
		read <- total
	}()

	// The read has fetched nothing yet; record while it is held open.
	<-backend.entered
	if err := store.RecordUsage(ctx, newPAYGRecord("user-1", "org-1", 250)); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	backend.release <- struct{}{}

	if got := <-read; got != 1_000 {
		t.Errorf("the in-flight read returned %d, want the 1000 it was answered with", got)
	}

	// The stale total must not have been cached: the next check goes back to
	// the backend and sees the record.
	total, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, "org-1", "org-1", since)
	if err != nil {
		t.Fatalf("SumQuotaCostSince: %v", err)
	}
	if total != 1_250 {
		t.Errorf("total = %d, want 1250: the total read before the record was cached anyway", total)
	}
}

// TestUsageStore_KeepsTotalReadWithoutConcurrentRecord is the other half: a
// read nothing interfered with must still be cached, or the cache would never
// serve anything under load.
func TestUsageStore_KeepsTotalReadWithoutConcurrentRecord(t *testing.T) {
	ctx := context.Background()
	backend := &blockingUsageStore{
		total:    1_000,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		blockOne: true,
	}
	store := NewUsageStore(backend, NewMemoryCache(64), time.Minute)
	since := model.StartOfDay(time.Now())

	read := make(chan struct{})
	go func() {
		if _, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, "org-1", "org-1", since); err != nil {
			t.Errorf("SumQuotaCostSince: %v", err)
		}
		close(read)
	}()

	<-backend.entered
	backend.release <- struct{}{}
	<-read

	// A record landing after the read was stored is applied in place, so the
	// total stays exact without going back to the backend.
	if err := store.RecordUsage(ctx, newPAYGRecord("user-1", "org-1", 250)); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	backend.total = 999_999 // any backend read from here on would be visible
	total, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, "org-1", "org-1", since)
	if err != nil {
		t.Fatalf("SumQuotaCostSince: %v", err)
	}
	if total != 1_250 {
		t.Errorf("total = %d, want 1250 served from the cache", total)
	}
}

// TestUsageStore_ConcurrentReadsAndRecords runs the two paths against each
// other under -race, and checks that whatever the interleaving, the cached
// total never exceeds what was actually spent. Overshooting is the failure
// that refuses requests a fresh read would allow.
func TestUsageStore_ConcurrentReadsAndRecords(t *testing.T) {
	ctx := context.Background()
	backend := &countingUsageStore{}
	store := NewUsageStore(backend, NewMemoryCache(64), time.Minute)
	since := model.StartOfDay(time.Now())

	const records = 200

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < records; i++ {
			if err := store.RecordUsage(ctx, newPAYGRecord("user-1", "org-1", 10)); err != nil {
				t.Errorf("RecordUsage: %v", err)
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < records; i++ {
			total, err := store.SumQuotaCostSince(ctx, model.QuotaScopeOrg, "org-1", "org-1", since)
			if err != nil {
				t.Errorf("SumQuotaCostSince: %v", err)
				return
			}
			if total > records*10 {
				t.Errorf("total = %d, above the %d actually spent: a record was counted twice", total, records*10)
				return
			}
		}
	}()

	wg.Wait()
}
