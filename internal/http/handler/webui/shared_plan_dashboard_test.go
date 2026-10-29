package webui

import (
	"context"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	orgcomponent "github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
)

// sharedPlanUsageStore answers the per-model sums of a shared constraint, for
// the whole plan or for one user.
type sharedPlanUsageStore struct {
	port.UsageStore

	value     map[model.UserID]map[model.LLMModelID]int64
	plainSums int
}

func (s *sharedPlanUsageStore) SumPlanUsageByModelSince(_ context.Context, _ model.PlanUsageScope, userID model.UserID, _ time.Time) (map[model.LLMModelID]port.PlanUsageTotals, error) {
	totals := map[model.LLMModelID]port.PlanUsageTotals{}
	for user, byModel := range s.value {
		if userID != "" && user != userID {
			continue
		}
		for id, v := range byModel {
			t := totals[id]
			t.Value += v
			totals[id] = t
		}
	}
	return totals, nil
}

func (s *sharedPlanUsageStore) SumPlanUsageSince(context.Context, model.PlanUsageScope, time.Time) (int64, int64, error) {
	s.plainSums++
	return 0, 0, nil
}

func (s *sharedPlanUsageStore) SumUserPlanUsageSince(context.Context, model.UserID, model.PlanUsageScope, time.Time) (int64, int64, error) {
	s.plainSums++
	return 0, 0, nil
}

func (s *sharedPlanUsageStore) CountActivePlanUsersSince(context.Context, model.PlanUsageScope, time.Time, model.UserID) (int64, error) {
	return 2, nil
}

func (s *sharedPlanUsageStore) HasPlanUsageSince(context.Context, model.UserID, model.PlanUsageScope, time.Time) (bool, error) {
	return true, nil
}

func (s *sharedPlanUsageStore) EarliestPlanUsageSince(context.Context, model.PlanUsageScope, time.Time) (time.Time, error) {
	return time.Time{}, nil
}

func TestFillDashboardRollingWindowUsage_SharedConstraintWeighsTheViewer(t *testing.T) {
	budget := int64(60_000_000)
	glmBudget := int64(180_000_000)
	c := model.PlanConstraint{
		Kind:         model.ConstraintRollingWindow,
		Label:        "Mois",
		Duration:     model.PlanDuration(720 * time.Hour),
		ValueBudget:  &budget,
		Scope:        model.PlanScopeShared,
		ModelBudgets: map[string]model.PlanBudget{"glm-5.3-flash": {ValueBudget: &glmBudget}},
	}
	glm := model.NewLLMModel("prov-1", "org-1", "glm", "glm-5.3-flash", "", 0, 0)
	kimi := model.NewLLMModel("prov-1", "org-1", "kimi", "kimi-k3", "", 0, 0)

	store := &sharedPlanUsageStore{value: map[model.UserID]map[model.LLMModelID]int64{
		"alice": {glm.ID(): 60_000_000},
		"bob":   {kimi.ID(): 15_000_000},
	}}
	h := &Handler{usageStore: store, fairShare: service.NewFairShareService(store)}

	cu := orgcomponent.SubscriptionConstraintUsage{Constraint: c}
	h.fillDashboardRollingWindowUsage(context.Background(), &cu, c, model.PlanUsageScope{OrgID: "org-1", ProviderID: "prov-1"},
		[]model.LLMModel{glm, kimi}, "alice", 2, time.Now())

	// Alice's 60$ of glm weigh 20$, against an allowance decided on the
	// weighed plan usage.
	if cu.ValueUsed != 20_000_000 {
		t.Errorf("ValueUsed = %d, want 20000000: the viewer's usage weighed by model", cu.ValueUsed)
	}
	if cu.ValueMode == "" || cu.ActiveUsers != 2 {
		t.Errorf("share = (%q, %d active), want the allocator's", cu.ValueMode, cu.ActiveUsers)
	}
	if store.plainSums != 0 {
		t.Errorf("%d unweighted sums read, want none on a shared constraint", store.plainSums)
	}
}
