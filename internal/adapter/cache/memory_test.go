package cache

import (
	"context"
	"testing"
	"time"
)

func TestMemoryCache_GetSet(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryCache(16)

	if _, exists, err := c.GetInt64(ctx, "absent"); err != nil || exists {
		t.Fatalf("absent key: exists=%v err=%v", exists, err)
	}

	if err := c.SetInt64(ctx, "k", 42, time.Minute); err != nil {
		t.Fatalf("SetInt64: %v", err)
	}
	value, exists, err := c.GetInt64(ctx, "k")
	if err != nil || !exists || value != 42 {
		t.Fatalf("GetInt64 = %d, %v, %v; want 42, true, nil", value, exists, err)
	}

	if err := c.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, exists, _ := c.GetInt64(ctx, "k"); exists {
		t.Error("key still present after Delete")
	}
}

// TestMemoryCache_AddOnlyUpdatesPresentEntries pins the contract the quota
// totals depend on: an increment on a key nobody has read stores nothing. A
// counter built from increments alone would be missing everything spent before
// the process started.
func TestMemoryCache_AddOnlyUpdatesPresentEntries(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryCache(16)

	value, exists, err := c.AddInt64(ctx, "k", 10)
	if err != nil {
		t.Fatalf("AddInt64: %v", err)
	}
	if exists {
		t.Errorf("AddInt64 on an absent key reported exists=true (value %d)", value)
	}
	if _, present, _ := c.GetInt64(ctx, "k"); present {
		t.Error("AddInt64 created an entry for an absent key")
	}

	_ = c.SetInt64(ctx, "k", 100, time.Minute)
	if value, exists, _ = c.AddInt64(ctx, "k", 10); !exists || value != 110 {
		t.Errorf("AddInt64 = %d, %v; want 110, true", value, exists)
	}
	if value, _, _ = c.GetInt64(ctx, "k"); value != 110 {
		t.Errorf("stored value = %d, want 110", value)
	}
}

// TestMemoryCache_AddDoesNotExtendTTL is the reason the expiry is held here
// rather than delegated to an expiring LRU: a counter kept warm by increments
// must still age out and be re-read from the database, otherwise a replica
// would never see what the others spent.
func TestMemoryCache_AddDoesNotExtendTTL(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryCache(16)

	if err := c.SetInt64(ctx, "k", 100, 20*time.Millisecond); err != nil {
		t.Fatalf("SetInt64: %v", err)
	}
	for i := 0; i < 4; i++ {
		time.Sleep(8 * time.Millisecond)
		_, _, _ = c.AddInt64(ctx, "k", 1)
	}

	if _, exists, _ := c.GetInt64(ctx, "k"); exists {
		t.Error("entry survived its TTL because increments kept renewing it")
	}
}

func TestMemoryCache_Expires(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryCache(16)

	_ = c.SetInt64(ctx, "k", 1, 10*time.Millisecond)
	if _, exists, _ := c.GetInt64(ctx, "k"); !exists {
		t.Fatal("entry expired immediately")
	}
	time.Sleep(20 * time.Millisecond)
	if _, exists, _ := c.GetInt64(ctx, "k"); exists {
		t.Error("entry outlived its TTL")
	}
}

// TestMemoryCache_ZeroTTLNeverExpires covers the port contract: a zero TTL
// stores the entry without a deadline, subject only to LRU eviction.
func TestMemoryCache_ZeroTTLNeverExpires(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryCache(16)

	_ = c.SetInt64(ctx, "k", 7, 0)
	time.Sleep(10 * time.Millisecond)
	if value, exists, _ := c.GetInt64(ctx, "k"); !exists || value != 7 {
		t.Errorf("GetInt64 = %d, %v; want 7, true", value, exists)
	}
}
