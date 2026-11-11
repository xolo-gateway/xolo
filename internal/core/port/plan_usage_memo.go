package port

import (
	"context"
	"sync"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// PlanUsageMemo keeps, for one proxied request, what a UsageStore read of a
// plan's usage, so that the reads of the same window share it.
//
// The plan checks of a request read each window of a plan several times: the
// plan's total, the user's part of it, its split over the keys of a pool, by
// model or not. A store that supports it reads a window once, with the part
// of UserID, and serves the other reads of that window from it (#236). A read
// for another user, or a store that ignores the memo, reads as usual.
//
// The memo lives as long as the request's context: what it holds is the
// usage at the start of the request, which is what its checks decide on.
type PlanUsageMemo struct {
	// UserID is the user of the request, whose part of each window the memo
	// keeps. Empty for a request without a user.
	UserID model.UserID

	mu      sync.Mutex
	entries map[string]any
}

type planUsageMemoKey struct{}

// WithPlanUsageMemo returns a context carrying a new, empty PlanUsageMemo
// for a request of userID.
func WithPlanUsageMemo(ctx context.Context, userID model.UserID) context.Context {
	return context.WithValue(ctx, planUsageMemoKey{}, &PlanUsageMemo{UserID: userID, entries: map[string]any{}})
}

// PlanUsageMemoFrom returns the memo of ctx, or nil.
func PlanUsageMemoFrom(ctx context.Context) *PlanUsageMemo {
	memo, _ := ctx.Value(planUsageMemoKey{}).(*PlanUsageMemo)
	return memo
}

// Load returns the entry stored under key.
func (m *PlanUsageMemo) Load(key string) (any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.entries[key]
	return v, ok
}

// Store keeps value under key.
func (m *PlanUsageMemo) Store(key string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = value
}
