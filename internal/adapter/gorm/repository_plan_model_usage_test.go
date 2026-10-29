package gorm_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

func TestUsageStore_PerModelPlanUsage(t *testing.T) {
	eachBackend(t, scenarioUsageStorePerModelPlanUsage)
}

// scenarioUsageStorePerModelPlanUsage checks that a per-model plan scope
// counts its models alone, and that the per-model breakdown agrees with it.
func scenarioUsageStorePerModelPlanUsage(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()
	orgID, providerID := model.NewOrgID(), model.NewProviderID()
	userA, userB := model.NewUserID(), model.NewUserID()
	kimi, kimiAlias, glm := model.NewLLMModelID(), model.NewLLMModelID(), model.NewLLMModelID()

	planned := func(user model.UserID, modelID model.LLMModelID, providerCost int64) *model.BaseUsageRecord {
		r := model.NewUsageRecord(user, "", orgID, "", providerID, modelID,
			"m", "", 100, 0, 50, 0, "USD", model.CostSourceComputed, "")
		r.SetPlanCovered(true)
		r.SetProviderCost(providerCost)
		return r
	}
	for i, r := range []*model.BaseUsageRecord{
		planned(userA, kimi, 1_000),
		planned(userB, kimiAlias, 2_000),
		planned(userA, glm, 4_000),
	} {
		if err := store.RecordUsage(ctx, r); err != nil {
			t.Fatalf("RecordUsage(%d): %v", i, err)
		}
	}

	since := time.Now().Add(-time.Hour)
	providerScope := model.PlanUsageScope{OrgID: orgID, ProviderID: providerID}
	kimiScope := model.PlanUsageScope{OrgID: orgID, ProviderID: providerID, ModelIDs: []model.LLMModelID{kimi, kimiAlias}}
	glmScope := model.PlanUsageScope{OrgID: orgID, ProviderID: providerID, ModelIDs: []model.LLMModelID{glm}}

	tokens, value, err := store.SumPlanUsageSince(ctx, kimiScope, since)
	if err != nil {
		t.Fatalf("SumPlanUsageSince: %v", err)
	}
	if tokens != 300 || value != 3_000 {
		t.Errorf("kimi scope: got tokens=%d value=%d, want 300 and 3000", tokens, value)
	}

	if _, value, err := store.SumUserPlanUsageSince(ctx, userA, kimiScope, since); err != nil || value != 1_000 {
		t.Errorf("SumUserPlanUsageSince(user A, kimi) = %d, %v; want 1000", value, err)
	}

	if count, err := store.CountActivePlanUsersSince(ctx, glmScope, since, ""); err != nil || count != 1 {
		t.Errorf("CountActivePlanUsersSince(glm) = %d, %v; want 1", count, err)
	}

	if present, err := store.HasPlanUsageSince(ctx, userB, glmScope, since); err != nil || present {
		t.Errorf("HasPlanUsageSince(user B, glm) = %v, %v; want false", present, err)
	}

	if earliest, err := store.EarliestPlanUsageSince(ctx, glmScope, "", since); err != nil || earliest.IsZero() {
		t.Errorf("EarliestPlanUsageSince(glm) = %v, %v; want a usage time", earliest, err)
	}

	byModel, err := store.SumPlanUsageByModelSince(ctx, providerScope, "", since)
	if err != nil {
		t.Fatalf("SumPlanUsageByModelSince: %v", err)
	}
	want := map[model.LLMModelID]port.PlanUsageTotals{
		kimi:      {Tokens: 150, Value: 1_000},
		kimiAlias: {Tokens: 150, Value: 2_000},
		glm:       {Tokens: 150, Value: 4_000},
	}
	if !reflect.DeepEqual(byModel, want) {
		t.Errorf("SumPlanUsageByModelSince = %v, want %v", byModel, want)
	}

	byModel, err = store.SumPlanUsageByModelSince(ctx, providerScope, userB, since)
	if err != nil {
		t.Fatalf("SumPlanUsageByModelSince (user B): %v", err)
	}
	if len(byModel) != 1 || byModel[kimiAlias].Value != 2_000 {
		t.Errorf("SumPlanUsageByModelSince (user B) = %v, want the alias alone", byModel)
	}
}
