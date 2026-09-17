//go:build integration

package gorm_test

import (
	"context"
	"sync"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TestQuotaUsageWriteContention measures what the counters cost on the write
// path when a whole organization writes to the same row.
//
// The counter row (org, <org>, currency, day) is unique per organization and
// per day, so every usage record of that organization updates it and holds a
// row lock until COMMIT. That is the shape of the load in issue #44: one
// organization at a high request rate. The test writes the same number of
// records twice, once concentrated on one organization and once spread over
// distinct ones, and reports both rates.
//
// It guards against collapse rather than pinning a figure: a lock handed from
// one short transaction to the next costs something, but an order of magnitude
// would mean the scan was traded for a queue.
func TestQuotaUsageWriteContention(t *testing.T) {
	const (
		writers          = 16
		recordsPerWriter = 40
	)

	measure := func(t *testing.T, sameOrg bool) time.Duration {
		t.Helper()

		store := postgresBackends(t)[0].newStore(t)
		ctx := context.Background()
		sharedOrg := model.NewOrgID()

		var wg sync.WaitGroup
		errs := make(chan error, writers)
		start := time.Now()

		for w := 0; w < writers; w++ {
			orgID := sharedOrg
			if !sameOrg {
				orgID = model.NewOrgID()
			}
			userID := model.NewUserID()

			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < recordsPerWriter; i++ {
					record := model.NewUsageRecord(userID, "", orgID, "provider", "llm-model",
						"fast", "", 10, 0, 10, 100, "USD", model.CostSourceComputed, "")
					if err := store.RecordUsage(ctx, record); err != nil {
						errs <- err
						return
					}
				}
			}()
		}

		wg.Wait()
		elapsed := time.Since(start)
		close(errs)
		for err := range errs {
			t.Fatalf("RecordUsage: %v", err)
		}

		if sameOrg {
			assertOrgCounter(t, store, sharedOrg, int64(writers*recordsPerWriter*100))
		}
		return elapsed
	}

	spread := measure(t, false)
	concentrated := measure(t, true)

	total := writers * recordsPerWriter
	t.Logf("spread over %d organizations: %v for %d records (%.0f rec/s)",
		writers, spread.Round(time.Millisecond), total, float64(total)/spread.Seconds())
	t.Logf("concentrated on one organization: %v for %d records (%.0f rec/s)",
		concentrated.Round(time.Millisecond), total, float64(total)/concentrated.Seconds())
	t.Logf("ratio: %.2fx", float64(concentrated)/float64(spread))

	if concentrated > 10*spread {
		t.Errorf("writes to one organization are %.1fx slower than spread writes: the shared counter row serializes them",
			float64(concentrated)/float64(spread))
	}
}

// assertOrgCounter checks that no increment was lost to a concurrent one, the
// failure the upsert exists to prevent.
func assertOrgCounter(t *testing.T, store *xologorm.Store, orgID model.OrgID, want int64) {
	t.Helper()

	got, err := store.SumQuotaCostSince(context.Background(), model.QuotaScopeOrg, string(orgID), orgID, model.StartOfDay(time.Now()))
	if err != nil {
		t.Fatalf("SumQuotaCostSince: %v", err)
	}
	if got != want {
		t.Errorf("org counter = %d, want %d: concurrent increments were lost", got, want)
	}
}
