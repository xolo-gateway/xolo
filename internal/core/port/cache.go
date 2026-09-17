package port

import (
	"context"
	"time"
)

// Cache is a small key/value store for values that may be recomputed at any
// time. It is deliberately narrow, so a deployment spanning several gateway
// replicas can swap the in-process implementation for a shared one without
// touching the callers. Reads and writes are one Redis command each; AddInt64
// is the exception, since INCRBY creates the key it is missing and this
// contract must not, so a Redis implementation needs a short Lua script that
// increments only an existing key and leaves its expiry alone.
//
// A cache is best-effort by contract. An implementation that loses an entry,
// or that cannot be reached, must report a miss rather than an error the caller
// has to handle; callers recompute from the source of truth and carry on.
type Cache interface {
	// GetInt64 returns the value stored under key. The second result is false
	// when the key is absent or expired.
	GetInt64(ctx context.Context, key string) (int64, bool, error)
	// SetInt64 stores value under key for ttl. A ttl of zero stores it without
	// expiry, subject to the implementation's own eviction.
	SetInt64(ctx context.Context, key string, value int64, ttl time.Duration) error
	// AddInt64 adds delta to the value stored under key and returns the result.
	// It only ever updates an entry that is already there: a miss reports false
	// and stores nothing, because an increment on its own says nothing about the
	// total it belongs to. The entry keeps the expiry it was created with, so a
	// counter kept up to date this way still ages out and is re-read from the
	// source of truth.
	AddInt64(ctx context.Context, key string, delta int64) (int64, bool, error)
	// Delete removes the given keys. Absent keys are not an error.
	Delete(ctx context.Context, keys ...string) error
}
