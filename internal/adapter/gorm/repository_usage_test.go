package gorm_test

import (
	"context"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
)

func TestQuotaStore_SetGetAndResolve(t *testing.T) {
	eachBackend(t, scenarioQuotaStoreSetGetAndResolve)
}

func scenarioQuotaStoreSetGetAndResolve(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()

	orgID := model.NewOrgID()
	userID := model.NewUserID()

	if _, err := store.GetQuota(ctx, model.QuotaScopeOrg, string(orgID)); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("GetQuota (unset): expected port.ErrNotFound, got %v", err)
	}

	orgQuota := model.NewQuota(model.QuotaScopeOrg, string(orgID), "EUR", ptr(int64(1_000)), ptr(int64(20_000)), nil)
	if err := store.SetQuota(ctx, orgQuota); err != nil {
		t.Fatalf("SetQuota (org): %v", err)
	}

	loaded, err := store.GetQuota(ctx, model.QuotaScopeOrg, string(orgID))
	if err != nil {
		t.Fatalf("GetQuota: %v", err)
	}
	if loaded.Currency() != "EUR" {
		t.Errorf("expected currency EUR, got %q", loaded.Currency())
	}
	if loaded.DailyBudget() == nil || *loaded.DailyBudget() != 1_000 {
		t.Errorf("expected a daily budget of 1000, got %v", loaded.DailyBudget())
	}
	if loaded.YearlyBudget() != nil {
		t.Errorf("expected an unlimited yearly budget, got %v", *loaded.YearlyBudget())
	}

	// SetQuota is an upsert keyed on (scope, scope_id), not an insert.
	updated := model.NewQuota(model.QuotaScopeOrg, string(orgID), "EUR", ptr(int64(500)), ptr(int64(20_000)), nil)
	if err := store.SetQuota(ctx, updated); err != nil {
		t.Fatalf("SetQuota (update): %v", err)
	}
	loaded, err = store.GetQuota(ctx, model.QuotaScopeOrg, string(orgID))
	if err != nil {
		t.Fatalf("GetQuota (after update): %v", err)
	}
	if loaded.DailyBudget() == nil || *loaded.DailyBudget() != 500 {
		t.Errorf("expected the daily budget to be updated to 500, got %v", loaded.DailyBudget())
	}

	// The effective quota takes the tighter of the user and org budgets at
	// each period, and keeps the org budget where the user has none.
	userQuota := model.NewQuota(model.QuotaScopeUser, string(userID), "EUR", ptr(int64(800)), ptr(int64(5_000)), nil)
	if err := store.SetQuota(ctx, userQuota); err != nil {
		t.Fatalf("SetQuota (user): %v", err)
	}

	effective, err := store.ResolveEffectiveQuota(ctx, userID, orgID)
	if err != nil {
		t.Fatalf("ResolveEffectiveQuota: %v", err)
	}
	if effective.DailyBudget == nil || *effective.DailyBudget != 500 {
		t.Errorf("expected the org daily budget (500) to win, got %v", effective.DailyBudget)
	}
	if effective.MonthlyBudget == nil || *effective.MonthlyBudget != 5_000 {
		t.Errorf("expected the user monthly budget (5000) to win, got %v", effective.MonthlyBudget)
	}
	if effective.YearlyBudget != nil {
		t.Errorf("expected an unlimited yearly budget, got %v", *effective.YearlyBudget)
	}
}

// usageFixture is a set of records covering both billing modes, spread over
// two users, two providers and two currencies.
type usageFixture struct {
	orgID       model.OrgID
	otherOrgID  model.OrgID
	userA       model.UserID
	userB       model.UserID
	appID       model.ApplicationID
	providerID  model.ProviderID
	otherProvID model.ProviderID
	modelID     model.LLMModelID
}

func seedUsage(t *testing.T, store *xologorm.Store) usageFixture {
	t.Helper()

	ctx := context.Background()
	f := usageFixture{
		orgID:       model.NewOrgID(),
		otherOrgID:  model.NewOrgID(),
		userA:       model.NewUserID(),
		userB:       model.NewUserID(),
		appID:       model.NewApplicationID(),
		providerID:  model.NewProviderID(),
		otherProvID: model.NewProviderID(),
		modelID:     model.NewLLMModelID(),
	}

	payg := func(user model.UserID, orgID model.OrgID, providerID model.ProviderID, proxyName, resolved, currency string, cost int64) *model.BaseUsageRecord {
		return model.NewUsageRecord(user, "", orgID, providerID, f.modelID,
			proxyName, "", 100, 10, 50, cost, currency, model.CostSourceComputed, resolved)
	}

	records := []*model.BaseUsageRecord{
		payg(f.userA, f.orgID, f.providerID, "fast", "", "USD", 1_000),
		payg(f.userA, f.orgID, f.providerID, "fast", "gpt-4o-mini", "USD", 2_000),
		payg(f.userB, f.orgID, f.otherProvID, "smart", "", "EUR", 4_000),
		// Another org's usage must never bleed into the aggregates.
		payg(f.userA, f.otherOrgID, f.providerID, "fast", "", "USD", 9_999),
	}

	// A record attributed to an application rather than a user.
	records = append(records, model.NewUsageRecord("", f.appID, f.orgID, f.providerID, f.modelID,
		"fast", "", 100, 10, 50, 8_000, "USD", model.CostSourceComputed, ""))

	// A subscription-covered record: excluded from every PAYG aggregate.
	planned := payg(f.userA, f.orgID, f.providerID, "fast", "", "USD", 0)
	planned.SetPlanCovered(true)
	planned.SetProviderCost(750)
	records = append(records, planned)

	for i, r := range records {
		if err := store.RecordUsage(ctx, r); err != nil {
			t.Fatalf("RecordUsage(%d): %v", i, err)
		}
	}

	return f
}

func TestUsageStore_QueryAndAggregate(t *testing.T) {
	eachBackend(t, scenarioUsageStoreQueryAndAggregate)
}

func scenarioUsageStoreQueryAndAggregate(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()
	f := seedUsage(t, store)

	records, err := store.QueryUsage(ctx, port.UsageFilter{OrgID: &f.orgID})
	if err != nil {
		t.Fatalf("QueryUsage: %v", err)
	}
	if len(records) != 5 {
		t.Fatalf("expected 5 records for the org, got %d", len(records))
	}

	// QueryUsage returns the most recent records first.
	for i := 1; i < len(records); i++ {
		if records[i-1].CreatedAt().Before(records[i].CreatedAt()) {
			t.Errorf("expected records ordered by descending creation time, got %v then %v",
				records[i-1].CreatedAt(), records[i].CreatedAt())
			break
		}
	}

	filtered, err := store.QueryUsage(ctx, port.UsageFilter{OrgID: &f.orgID, UserID: &f.userB})
	if err != nil {
		t.Fatalf("QueryUsage (by user): %v", err)
	}
	if len(filtered) != 1 {
		t.Errorf("expected 1 record for user B, got %d", len(filtered))
	}

	planOnly, err := store.QueryUsage(ctx, port.UsageFilter{OrgID: &f.orgID, PlanCovered: ptr(true)})
	if err != nil {
		t.Fatalf("QueryUsage (plan covered): %v", err)
	}
	if len(planOnly) != 1 {
		t.Errorf("expected 1 subscription-covered record, got %d", len(planOnly))
	}

	limited, err := store.QueryUsage(ctx, port.UsageFilter{OrgID: &f.orgID, Limit: ptr(2)})
	if err != nil {
		t.Fatalf("QueryUsage (limit): %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("expected the limit to be honored, got %d records", len(limited))
	}

	aggregate, err := store.AggregateUsage(ctx, port.UsageFilter{OrgID: &f.orgID})
	if err != nil {
		t.Fatalf("AggregateUsage: %v", err)
	}
	if aggregate.TotalRequests != 5 {
		t.Errorf("expected 5 requests, got %d", aggregate.TotalRequests)
	}
	if aggregate.PromptTokens != 500 || aggregate.CompletionTokens != 250 || aggregate.TotalTokens != 750 {
		t.Errorf("unexpected token totals: prompt=%d completion=%d total=%d",
			aggregate.PromptTokens, aggregate.CompletionTokens, aggregate.TotalTokens)
	}
	if aggregate.CachedTokens != 50 {
		t.Errorf("expected 50 cached tokens, got %d", aggregate.CachedTokens)
	}
}

// TestUsageStore_PrincipalFilters covers the branch that mixes a user filter
// and an application filter: usage is attributed to either a user or an
// application, so combining both must widen the result set, not empty it.
func TestUsageStore_PrincipalFilters(t *testing.T) {
	eachBackend(t, scenarioUsageStorePrincipalFilters)
}

func scenarioUsageStorePrincipalFilters(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()
	f := seedUsage(t, store)

	tests := []struct {
		name     string
		filter   port.UsageFilter
		expected int
	}{
		{
			name:     "single user",
			filter:   port.UsageFilter{OrgID: &f.orgID, UserID: &f.userA},
			expected: 3, // 2 PAYG + 1 subscription-covered
		},
		{
			name:     "single application",
			filter:   port.UsageFilter{OrgID: &f.orgID, ApplicationID: &f.appID},
			expected: 1,
		},
		{
			name:     "several users",
			filter:   port.UsageFilter{OrgID: &f.orgID, UserIDs: []model.UserID{f.userA, f.userB}},
			expected: 4,
		},
		{
			name:     "user and application combined",
			filter:   port.UsageFilter{OrgID: &f.orgID, UserID: &f.userB, ApplicationID: &f.appID},
			expected: 2,
		},
		{
			name: "single and slice forms combined",
			filter: port.UsageFilter{
				OrgID:          &f.orgID,
				UserID:         &f.userA,
				UserIDs:        []model.UserID{f.userA, f.userB},
				ApplicationIDs: []model.ApplicationID{f.appID},
			},
			expected: 5,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records, err := store.QueryUsage(ctx, test.filter)
			if err != nil {
				t.Fatalf("QueryUsage: %v", err)
			}
			if len(records) != test.expected {
				t.Errorf("expected %d records, got %d", test.expected, len(records))
			}

			aggregate, err := store.AggregateUsage(ctx, test.filter)
			if err != nil {
				t.Fatalf("AggregateUsage: %v", err)
			}
			if aggregate.TotalRequests != int64(test.expected) {
				t.Errorf("expected %d aggregated requests, got %d", test.expected, aggregate.TotalRequests)
			}
		})
	}
}

func TestUsageStore_CostSums(t *testing.T) {
	eachBackend(t, scenarioUsageStoreCostSums)
}

func scenarioUsageStoreCostSums(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()
	f := seedUsage(t, store)
	since := time.Now().Add(-time.Hour)

	// Subscription-covered records are excluded, so user A's PAYG spend is
	// 1000 + 2000 in their own org only.
	sum, err := store.SumCostSince(ctx, f.userA, f.orgID, since)
	if err != nil {
		t.Fatalf("SumCostSince: %v", err)
	}
	if sum != 3_000 {
		t.Errorf("expected a PAYG total of 3000, got %d", sum)
	}

	byCurrency, err := store.SumCostSinceByCurrency(ctx, nil, f.orgID, since)
	if err != nil {
		t.Fatalf("SumCostSinceByCurrency: %v", err)
	}
	// Every USD PAYG record of the org, including the application's.
	if byCurrency["USD"] != 11_000 {
		t.Errorf("expected 11000 USD, got %d", byCurrency["USD"])
	}
	if byCurrency["EUR"] != 4_000 {
		t.Errorf("expected 4000 EUR, got %d", byCurrency["EUR"])
	}

	scoped, err := store.SumCostSinceByCurrency(ctx, []model.UserID{f.userB}, f.orgID, since)
	if err != nil {
		t.Fatalf("SumCostSinceByCurrency (scoped): %v", err)
	}
	if _, ok := scoped["USD"]; ok {
		t.Error("expected user B's totals to exclude USD")
	}
	if scoped["EUR"] != 4_000 {
		t.Errorf("expected 4000 EUR for user B, got %d", scoped["EUR"])
	}

	// A window that starts after every record sums to nothing.
	future, err := store.SumCostSince(ctx, f.userA, f.orgID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("SumCostSince (future): %v", err)
	}
	if future != 0 {
		t.Errorf("expected 0 outside the window, got %d", future)
	}
}

func TestUsageStore_PlanUsage(t *testing.T) {
	eachBackend(t, scenarioUsageStorePlanUsage)
}

func scenarioUsageStorePlanUsage(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()
	f := seedUsage(t, store)
	since := time.Now().Add(-time.Hour)

	tokens, providerValue, err := store.SumPlanUsageSince(ctx, f.orgID, f.providerID, since)
	if err != nil {
		t.Fatalf("SumPlanUsageSince: %v", err)
	}
	if tokens != 150 {
		t.Errorf("expected 150 subscription-covered tokens, got %d", tokens)
	}
	if providerValue != 750 {
		t.Errorf("expected a provider value of 750, got %d", providerValue)
	}

	userTokens, userValue, err := store.SumUserPlanUsageSince(ctx, f.userB, f.orgID, f.providerID, since)
	if err != nil {
		t.Fatalf("SumUserPlanUsageSince: %v", err)
	}
	if userTokens != 0 || userValue != 0 {
		t.Errorf("expected no subscription usage for user B, got tokens=%d value=%d", userTokens, userValue)
	}

	earliest, err := store.EarliestPlanUsageSince(ctx, f.orgID, f.providerID, since)
	if err != nil {
		t.Fatalf("EarliestPlanUsageSince: %v", err)
	}
	if earliest.IsZero() {
		t.Error("expected a non-zero earliest subscription usage")
	}

	// No subscription usage in the window: a zero time, not an error.
	earliest, err = store.EarliestPlanUsageSince(ctx, f.orgID, f.providerID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("EarliestPlanUsageSince (future): %v", err)
	}
	if !earliest.IsZero() {
		t.Errorf("expected a zero time outside the window, got %v", earliest)
	}
}

func TestUsageStore_CountActivePlanUsers(t *testing.T) {
	eachBackend(t, scenarioUsageStoreCountActivePlanUsers)
}

func scenarioUsageStoreCountActivePlanUsers(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()
	f := usageFixture{
		orgID:       model.NewOrgID(),
		otherOrgID:  model.NewOrgID(),
		userA:       model.NewUserID(),
		userB:       model.NewUserID(),
		appID:       model.NewApplicationID(),
		providerID:  model.NewProviderID(),
		otherProvID: model.NewProviderID(),
		modelID:     model.NewLLMModelID(),
	}

	plan := func(user model.UserID, appID model.ApplicationID, orgID model.OrgID, providerID model.ProviderID) *model.BaseUsageRecord {
		r := model.NewUsageRecord(user, appID, orgID, providerID, f.modelID,
			"fast", "", 100, 10, 50, 0, "USD", model.CostSourceComputed, "")
		r.SetPlanCovered(true)
		r.SetProviderCost(100)
		return r
	}

	records := []*model.BaseUsageRecord{
		// Two records for the same user must count as one active user.
		plan(f.userA, "", f.orgID, f.providerID),
		plan(f.userA, "", f.orgID, f.providerID),
		plan(f.userB, "", f.orgID, f.providerID),
		// Application traffic has no user to share a budget with.
		plan("", f.appID, f.orgID, f.providerID),
		// Neither another provider nor another org may leak into the count.
		plan(model.NewUserID(), "", f.orgID, f.otherProvID),
		plan(model.NewUserID(), "", f.otherOrgID, f.providerID),
	}
	// PAYG traffic is not covered by the plan and must be ignored too.
	records = append(records, model.NewUsageRecord(model.NewUserID(), "", f.orgID, f.providerID, f.modelID,
		"fast", "", 100, 10, 50, 1_000, "USD", model.CostSourceComputed, ""))

	for i, r := range records {
		if err := store.RecordUsage(ctx, r); err != nil {
			t.Fatalf("RecordUsage(%d): %v", i, err)
		}
	}

	count, err := store.CountActivePlanUsersSince(ctx, f.orgID, f.providerID, time.Now().Add(-time.Hour), "")
	if err != nil {
		t.Fatalf("CountActivePlanUsersSince: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 active plan users, got %d", count)
	}

	// Excluding a user leaves the others: callers allocating for a user add them
	// back themselves, which is exact whether or not they have consumed yet.
	count, err = store.CountActivePlanUsersSince(ctx, f.orgID, f.providerID, time.Now().Add(-time.Hour), f.userA)
	if err != nil {
		t.Fatalf("CountActivePlanUsersSince (excluding userA): %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 active plan user once userA is excluded, got %d", count)
	}

	// Excluding a user who never consumed changes nothing.
	count, err = store.CountActivePlanUsersSince(ctx, f.orgID, f.providerID, time.Now().Add(-time.Hour), model.NewUserID())
	if err != nil {
		t.Fatalf("CountActivePlanUsersSince (excluding a stranger): %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 active plan users, got %d", count)
	}

	// Presence is the per-caller half of the same question.
	for _, tc := range []struct {
		user model.UserID
		want bool
	}{
		{f.userA, true},
		{f.userB, true},
		{model.NewUserID(), false},
	} {
		present, err := store.HasPlanUsageSince(ctx, tc.user, f.orgID, f.providerID, time.Now().Add(-time.Hour))
		if err != nil {
			t.Fatalf("HasPlanUsageSince(%s): %v", tc.user, err)
		}
		if present != tc.want {
			t.Errorf("HasPlanUsageSince(%s) = %v, want %v", tc.user, present, tc.want)
		}
	}
	// Another provider's usage does not make the user present on this one.
	if present, err := store.HasPlanUsageSince(ctx, f.userA, f.orgID, f.otherProvID, time.Now().Add(-time.Hour)); err != nil || present {
		t.Errorf("HasPlanUsageSince on the other provider = (%v, %v), want (false, nil)", present, err)
	}

	// Outside the window nobody is active.
	count, err = store.CountActivePlanUsersSince(ctx, f.orgID, f.providerID, time.Now().Add(time.Hour), "")
	if err != nil {
		t.Fatalf("CountActivePlanUsersSince (future): %v", err)
	}
	if count != 0 {
		t.Errorf("expected no active plan user outside the window, got %d", count)
	}
}
