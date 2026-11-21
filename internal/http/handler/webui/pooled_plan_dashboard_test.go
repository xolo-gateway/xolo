package webui

import (
	"context"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// pooledPlanUsageStore answers the plan sums from value totals per user and
// per key of a pool.
type pooledPlanUsageStore struct {
	port.UsageStore

	value map[model.UserID]map[model.ProviderKeyID]int64
}

func (s *pooledPlanUsageStore) sum(scope model.PlanUsageScope, user model.UserID) int64 {
	var total int64
	for u, byKey := range s.value {
		if user != "" && u != user {
			continue
		}
		for key, v := range byKey {
			if !scope.ByKey || key == scope.KeyID {
				total += v
			}
		}
	}
	return total
}

func (s *pooledPlanUsageStore) active(scope model.PlanUsageScope, user model.UserID) bool {
	byKey, ok := s.value[user]
	if !ok || !scope.ByKey {
		return ok
	}
	_, ok = byKey[scope.KeyID]
	return ok
}

func (s *pooledPlanUsageStore) SumPlanUsageSince(_ context.Context, scope model.PlanUsageScope, _ time.Time) (int64, int64, error) {
	return 0, s.sum(scope, ""), nil
}

func (s *pooledPlanUsageStore) SumUserPlanUsageSince(_ context.Context, user model.UserID, scope model.PlanUsageScope, _ time.Time) (int64, int64, error) {
	return 0, s.sum(scope, user), nil
}

func (s *pooledPlanUsageStore) CountActivePlanUsersSince(_ context.Context, scope model.PlanUsageScope, _ time.Time, exclude model.UserID) (int64, error) {
	var n int64
	for u := range s.value {
		if u != exclude && s.active(scope, u) {
			n++
		}
	}
	return n, nil
}

func (s *pooledPlanUsageStore) HasPlanUsageSince(_ context.Context, user model.UserID, scope model.PlanUsageScope, _ time.Time) (bool, error) {
	return s.active(scope, user), nil
}

func (s *pooledPlanUsageStore) EarliestPlanUsageSince(context.Context, model.PlanUsageScope, model.UserID, time.Time) (time.Time, error) {
	return time.Time{}, nil
}

// newPooledDashboardFixture serves a provider holding its own key and "k2",
// with a 10$ monthly window: the plan's window opened a day ago, k2's ten
// days ago.
func newPooledDashboardFixture(t *testing.T, now time.Time) (*Handler, *pooledPlanUsageStore, *model.BaseProvider) {
	t.Helper()
	planAnchor, keyAnchor := now.Add(-24*time.Hour), now.Add(-240*time.Hour)
	p := model.NewProvider("org-1", "OpenCode", "openai", "https://opencode.ai/zen/go/v1", "key", "USD")
	p.SetBillingMode(model.BillingModeSubscription)
	p.SetSubscriptionPlan(&model.SubscriptionPlan{Label: "Go", Constraints: []model.PlanConstraint{{
		Kind:         model.ConstraintRollingWindow,
		Label:        "Mois",
		Duration:     model.PlanDuration(720 * time.Hour),
		ValueBudget:  int64Ptr(10_000_000),
		WindowAnchor: &planAnchor,
	}}})
	p.SetExtraAPIKeys([]model.ProviderKey{{ID: "k2", Label: "member 2", APIKey: "key2",
		Overrides: map[string]model.PlanConstraintOverride{"Mois": {WindowAnchor: &keyAnchor}}}})

	store := &pooledPlanUsageStore{value: map[model.UserID]map[model.ProviderKeyID]int64{}}
	h := &Handler{usageStore: store, fairShare: service.NewFairShareService(store)}
	return h, store, p
}

func int64Ptr(v int64) *int64 { return &v }

func pooledGauges(t *testing.T, h *Handler, p *model.BaseProvider, now time.Time) (int64, time.Time) {
	t.Helper()
	rows := h.pooledDashboardUsage(context.Background(), p, model.PooledPlan(p), model.ProviderKeyIDs(p), nil, "alice", 2, now)
	if len(rows) != 1 {
		t.Fatalf("%d gauges, want one for the plan's one window", len(rows))
	}
	return rows[0].ValueUsed, rows[0].ResetAt
}

func TestPooledDashboardUsage_ShowsTheKeyTheViewerHasRoomOn(t *testing.T) {
	// Alice used her share of k2, whose window runs on; the provider's own
	// key, whose window just reset, holds hers whole.
	now := time.Now()
	h, store, p := newPooledDashboardFixture(t, now)
	store.value["alice"] = map[model.ProviderKeyID]int64{"k2": 6_000_000}
	store.value["bob"] = map[model.ProviderKeyID]int64{"k2": 1_000_000}

	used, reset := pooledGauges(t, h, p, now)
	if used != 0 {
		t.Errorf("ValueUsed = %d, want 0: the gauges of the key she has room on", used)
	}
	if want := now.Add(-24 * time.Hour).Add(720 * time.Hour); !reset.Equal(want) {
		t.Errorf("ResetAt = %s, want the provider's own window, %s", reset, want)
	}
}

func TestPooledDashboardUsage_ShowsTheKeyThatFreesUpFirst(t *testing.T) {
	// Alice used her share of both keys: she gets room back when k2's window,
	// opened ten days ago, resets, long before the plan's.
	now := time.Now()
	h, store, p := newPooledDashboardFixture(t, now)
	store.value["alice"] = map[model.ProviderKeyID]int64{model.PrimaryProviderKey: 6_000_000, "k2": 7_000_000}
	store.value["bob"] = map[model.ProviderKeyID]int64{model.PrimaryProviderKey: 1_000_000, "k2": 1_000_000}

	used, reset := pooledGauges(t, h, p, now)
	if used != 7_000_000 {
		t.Errorf("ValueUsed = %d, want k2's 7000000", used)
	}
	if want := now.Add(-240 * time.Hour).Add(720 * time.Hour); !reset.Equal(want) {
		t.Errorf("ResetAt = %s, want k2's window, %s, when she gets room back", reset, want)
	}
}
