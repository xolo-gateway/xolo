package gorm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"gorm.io/gorm"
)

func TestRetryStopsDuringBackoffWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	err := (&Store{retryBackoff: func(int) time.Duration { return 20 * time.Millisecond }}).retry(ctx, nil, false, func(context.Context, *gorm.DB) error {
		calls++
		if calls == 1 {
			cancel()
			return sqlite3.BUSY
		}
		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retry error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("retry callback calls = %d, want 1", calls)
	}
}

func TestRetryDoesNotCallBackForPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	err := (&Store{}).retry(ctx, nil, false, func(context.Context, *gorm.DB) error {
		calls++
		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retry error = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("retry callback calls = %d, want 0", calls)
	}
}

func TestWaitForRetryReturnsWhenContextIsAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	err := waitForRetry(ctx, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("canceled wait took %s, want less than 100ms", elapsed)
	}
}

func TestRetryWaitsAndRetriesWhenContextRemainsActive(t *testing.T) {
	calls := 0
	started := time.Now()
	err := (&Store{retryBackoff: func(int) time.Duration { return 5 * time.Millisecond }}).retry(context.Background(), nil, false, func(context.Context, *gorm.DB) error {
		calls++
		if calls == 1 {
			return sqlite3.BUSY
		}
		return nil
	})

	if err != nil {
		t.Fatalf("retry error = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("retry callback calls = %d, want 2", calls)
	}
	if elapsed := time.Since(started); elapsed < 5*time.Millisecond {
		t.Fatalf("retry waited %s, want at least 5ms", elapsed)
	}
}

func TestRetryDelayCapsDefaultBackoff(t *testing.T) {
	if got := (&Store{}).retryDelay(10); got != 30*time.Second {
		t.Fatalf("retry delay = %s, want 30s", got)
	}
}
