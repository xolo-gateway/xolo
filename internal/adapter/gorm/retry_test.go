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
	firstFailure := make(chan struct{})
	go func() {
		<-firstFailure
		time.Sleep(25 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	err := (&Store{}).retry(ctx, nil, false, func(context.Context, *gorm.DB) error {
		calls++
		if calls == 1 {
			close(firstFailure)
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
	if elapsed := time.Since(started); elapsed >= 250*time.Millisecond {
		t.Fatalf("canceled retry waited %s, want less than 250ms", elapsed)
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
	if elapsed := time.Since(started); elapsed >= 250*time.Millisecond {
		t.Fatalf("canceled wait took %s, want less than 250ms", elapsed)
	}
}

func TestRetryWaitsAndRetriesWhenContextRemainsActive(t *testing.T) {
	calls := 0
	started := time.Now()
	err := (&Store{}).retry(context.Background(), nil, false, func(context.Context, *gorm.DB) error {
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
	if elapsed := time.Since(started); elapsed < 450*time.Millisecond {
		t.Fatalf("retry waited %s, want at least 450ms", elapsed)
	}
}
