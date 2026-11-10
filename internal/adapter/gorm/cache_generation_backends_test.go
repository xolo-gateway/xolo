package gorm_test

import (
	"context"
	"sync"
	"testing"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	gormpkg "gorm.io/gorm"
)

func TestCacheGenerationsOnEachBackend(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		if err := db.AutoMigrate(&xologorm.CacheGeneration{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		store := xologorm.NewCacheGenerationStore(db, nil)
		ctx := context.Background()

		generations, err := store.Generations(ctx)
		if err != nil || len(generations) != 0 {
			t.Fatalf("expected no generation yet, got %v (%v)", generations, err)
		}

		// Concurrent bumps from several instances must each count: a lost
		// increment would leave an instance thinking nothing changed.
		const bumps = 8
		var wg sync.WaitGroup
		errs := make(chan error, bumps)
		for range bumps {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- store.Bump(ctx, "auth_tokens")
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("bump: %v", err)
			}
		}
		if err := store.Bump(ctx, "users"); err != nil {
			t.Fatalf("bump: %v", err)
		}

		generations, err = store.Generations(ctx)
		if err != nil {
			t.Fatalf("generations: %v", err)
		}
		if generations["auth_tokens"] != bumps || generations["users"] != 1 {
			t.Fatalf("unexpected generations %v", generations)
		}
	})
}
