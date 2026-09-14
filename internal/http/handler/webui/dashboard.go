package webui

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/adapter/cache"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/estimator"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	orgcomponent "github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/profile/component"
)

const dashboardPageSize = 20

func (h *Handler) getDashboardPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	memberships := httpCtx.Memberships(ctx)
	baseURL := httpCtx.BaseURL(ctx)

	// No memberships → redirect to /no-org
	if len(memberships) == 0 {
		http.Redirect(w, r, landingWithoutOrg(user, baseURL), http.StatusTemporaryRedirect)
		return
	}

	// Pagination
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			page = n
		}
	}
	offset := (page - 1) * dashboardPageSize

	// Time range
	rangeParam := r.URL.Query().Get("range")
	since := dashboardRangeToSince(rangeParam)

	// CSV export
	if r.URL.Query().Get("format") == "csv" {
		h.serveUserUsageCSV(w, r, user.ID(), since)
		return
	}

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())

	// Build per-org quota usage
	userID := user.ID()
	orgUsages := make([]component.OrgUsage, 0, len(memberships))
	for _, m := range memberships {
		quota, err := h.quotaService.ResolveEffectiveQuota(ctx, userID, m.OrgID())
		if err != nil {
			slog.ErrorContext(ctx, "could not resolve quota", slogx.Error(err), slog.String("orgID", string(m.OrgID())))
		}

		currency := model.DefaultCurrency
		if quota != nil {
			currency = quota.Currency
		}

		daily, err := h.sumCostConverted(ctx, userID, m.OrgID(), currency, todayStart)
		if err != nil {
			slog.ErrorContext(ctx, "could not sum daily cost", slogx.Error(err))
		}
		monthly, err := h.sumCostConverted(ctx, userID, m.OrgID(), currency, monthStart)
		if err != nil {
			slog.ErrorContext(ctx, "could not sum monthly cost", slogx.Error(err))
		}
		yearly, err := h.sumCostConverted(ctx, userID, m.OrgID(), currency, yearStart)
		if err != nil {
			slog.ErrorContext(ctx, "could not sum yearly cost", slogx.Error(err))
		}

		orgUsages = append(orgUsages, component.OrgUsage{
			Membership:  m,
			Quota:       quota,
			DailyCost:   daily,
			MonthlyCost: monthly,
			YearlyCost:  yearly,
			Currency:    currency,
		})
	}

	// Aggregate usage for the selected period (no currency filter: counts all records)
	agg, err := h.usageStore.AggregateUsage(ctx, port.UsageFilter{
		UserID: &userID,
		Since:  &since,
	})
	if err != nil {
		slog.ErrorContext(ctx, "could not aggregate usage", slogx.Error(err))
		agg = &port.UsageAggregate{}
	}

	// Compute total cost with per-currency conversion so mixed-currency records
	// (e.g. USD stored when EUR conversion failed) are properly included.
	if agg != nil && len(orgUsages) > 0 {
		// Use the first org's currency as the display currency for the total.
		displayCurrency := orgUsages[0].Currency
		var totalConverted int64
		for _, ou := range orgUsages {
			orgCur := ou.Currency
			converted, convErr := h.sumCostConverted(ctx, userID, ou.Membership.OrgID(), orgCur, since)
			if convErr != nil {
				slog.ErrorContext(ctx, "could not compute converted total", slogx.Error(convErr))
				continue
			}
			// If orgs have different currencies convert to display currency.
			if orgCur != displayCurrency {
				converted, convErr = h.exchangeRateService.Convert(ctx, converted, orgCur, displayCurrency)
				if convErr != nil {
					slog.WarnContext(ctx, "could not convert org total to display currency", slogx.Error(convErr))
				}
			}
			totalConverted += converted
		}
		agg.TotalCost = totalConverted
		agg.Currency = displayCurrency
	}

	// Paginated records
	records, err := h.usageStore.QueryUsage(ctx, port.UsageFilter{
		UserID: &userID,
		Since:  &since,
		Limit:  intPtr(dashboardPageSize + 1),
		Offset: intPtr(offset),
	})
	if err != nil {
		slog.ErrorContext(ctx, "could not query usage records", slogx.Error(err))
		records = nil
	}

	hasNext := len(records) > dashboardPageSize
	if hasNext {
		records = records[:dashboardPageSize]
	}

	// Build org name map for the table
	orgs := make(map[model.OrgID]model.Organization)
	for _, m := range memberships {
		if m.Org() != nil {
			orgs[m.OrgID()] = m.Org()
		}
	}

	// Cache key for energy totals
	energyCacheKey := fmt.Sprintf("dashboard:%s:%d", userID, since.Unix())

	// Filter matching the whole selected period, used by every chart aggregation.
	chartFilter := port.UsageFilter{
		UserID: &userID,
		Since:  &since,
	}

	// Build the model/provider caches for the *paginated* records only (bounded by the
	// page size, served by the provider store cache). Used for the per-row energy column.
	pageModelCache := make(map[model.LLMModelID]model.LLMModel)
	pageProviderCache := make(map[model.ProviderID]model.Provider)
	for _, rec := range records {
		mid := rec.ModelID()
		if mid == "" {
			continue
		}
		if _, ok := pageModelCache[mid]; ok {
			continue
		}
		m, err := h.providerStore.GetLLMModelByID(ctx, mid)
		if err != nil {
			continue
		}
		pageModelCache[mid] = m
		pid := m.ProviderID()
		if _, ok := pageProviderCache[pid]; !ok {
			if p, err := h.providerStore.GetProviderByID(ctx, pid); err == nil {
				pageProviderCache[pid] = p
			}
		}
	}

	// Cost charts are aggregated in SQL (GROUP BY) instead of loading every record of the
	// period into memory. Records may span several orgs, so each sub-total is converted
	// from its stored currency to *its own org's* currency, matching the previous per-record
	// behavior.
	convertPerOrg := func(rows []port.DimensionCost) map[string]int64 {
		out := make(map[string]int64, len(rows))
		for _, row := range rows {
			cost := row.Cost
			target := model.DefaultCurrency
			if org, ok := orgs[row.OrgID]; ok && org.Currency() != "" {
				target = org.Currency()
			}
			if row.Currency != target {
				if converted, convErr := h.exchangeRateService.Convert(ctx, row.Cost, row.Currency, target); convErr == nil {
					cost = converted
				}
			}
			out[row.Key] += cost
		}
		return out
	}

	perModel := convertPerOrg(h.aggregateCostRows(ctx, chartFilter, port.UsageDimensionModel))
	perDay := convertPerOrg(h.aggregateCostRows(ctx, chartFilter, port.UsageDimensionDay))

	perProvider := make(map[model.ProviderID]int64)
	for key, cost := range convertPerOrg(h.aggregateCostRows(ctx, chartFilter, port.UsageDimensionProvider)) {
		perProvider[model.ProviderID(key)] += cost
	}

	// Build provider name map for chart labels
	providerNames := make(map[model.ProviderID]string)
	for pid := range perProvider {
		if p, err := h.providerStore.GetProviderByID(ctx, pid); err == nil {
			providerNames[pid] = p.Name()
		} else {
			providerNames[pid] = string(pid)
		}
	}

	// Energy is non-linear per request and cannot be aggregated in SQL, so it is the only
	// figure that still needs the full record set — and only when its (short-TTL) cache
	// misses. On the common path (cache hit) no unbounded scan happens.
	var totalEnergyWh, totalCO2GramsMid float64
	if cached, ok := cache.EnergyEstimateCache.Get(energyCacheKey); ok {
		totalEnergyWh = cached.TotalEnergyWh
		totalCO2GramsMid = cached.TotalCO2GramsMid
	} else {
		totalEnergyWh, totalCO2GramsMid = h.computeEnergyTotals(ctx, chartFilter)
		cache.EnergyEstimateCache.Add(energyCacheKey, cache.EnergyTotals{
			TotalEnergyWh:    totalEnergyWh,
			TotalCO2GramsMid: totalCO2GramsMid,
		})
	}

	// Build display records with cost converted to org currency where needed
	displayRecords := make([]component.DisplayUsageRecord, 0, len(records))
	for _, rec := range records {
		displayModelName := rec.ProxyModelName()
		if rec.ResolvedModelName() != "" && rec.ResolvedModelName() != rec.ProxyModelName() {
			displayModelName = rec.ProxyModelName() + " → " + rec.ResolvedModelName()
		}
		dr := component.DisplayUsageRecord{
			Record:           rec,
			DisplayModelName: displayModelName,
			DisplayCost:      rec.Cost(),
			DisplayCurrency:  rec.Currency(),
		}
		if org, ok := orgs[rec.OrgID()]; ok {
			orgCurrency := org.Currency()
			if orgCurrency == "" {
				orgCurrency = model.DefaultCurrency
			}
			if orgCurrency != rec.Currency() {
				converted, convErr := h.exchangeRateService.Convert(ctx, rec.Cost(), rec.Currency(), orgCurrency)
				if convErr == nil {
					dr.DisplayCost = converted
					dr.DisplayCurrency = orgCurrency
					dr.Converted = true
				}
			}
		}
		// Energy estimation
		if m, ok := pageModelCache[rec.ModelID()]; ok && m.ActiveParams() > 0 {
			tier := estimator.TierHyperscaler
			if p, ok := pageProviderCache[m.ProviderID()]; ok {
				tier = estimator.CloudTier(p.CloudTier())
			}
			est := estimator.NewCloudEstimator(tier).EstimateFromParams(
				float64(m.ActiveParams()),
				estimator.InferenceRequest{
					InputTokens:  int(rec.PromptTokens()),
					OutputTokens: int(rec.CompletionTokens()),
				},
				m.TokensPerSecLow(),
				m.TokensPerSecHigh(),
			)
			dr.EnergyWh = est.Mid.TotalWh
			dr.EnergyLowWh = est.Low.TotalWh
			dr.EnergyHighWh = est.High.TotalWh
			dr.CO2GramsMid = est.Mid.Equivalences.CO2Grams
			dr.CO2GramsMin = est.Mid.Equivalences.CO2GramsMin
			dr.CO2GramsMax = est.Mid.Equivalences.CO2GramsMax
		}
		displayRecords = append(displayRecords, dr)
	}

	// Build subscription provider consumption for all the user's orgs, scoped to the
	// viewer's personal fair-share (usage and budgets divided by the org member count).
	var subscriptionProviders []orgcomponent.SubscriptionProviderUsage
	for _, m := range memberships {
		subscriptionProviders = append(subscriptionProviders, h.buildDashboardSubscriptionUsage(ctx, m.OrgID(), user.ID())...)
	}

	vmodel := component.DashboardPageVModel{
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "usage",
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: "Espace personnel", Href: "/usage"},
				{Label: "Usage", Href: ""},
			},
			Context: common.ContextPersonal,
		},
		OrgUsages:             orgUsages,
		SubscriptionProviders: subscriptionProviders,
		Aggregate:             agg,
		Records:               displayRecords,
		Orgs:                  orgs,
		Range:                 rangeParam,
		Page:                  page,
		PageSize:              dashboardPageSize,
		HasNext:               hasNext,
		ChartPerDay:           common.CostSeries(perDay, since, time.Now(), rangeParam),
		ChartSharesPerModel:   common.ChartShares(common.TopNChartDataPoints(dashChartByValue(perModel), 5)),
		ChartPerProvider:      dashChartByProvider(perProvider, providerNames),
		TotalEnergyWh:         totalEnergyWh,
		TotalCO2GramsMid:      totalCO2GramsMid,
	}

	templ.Handler(component.DashboardPage(vmodel)).ServeHTTP(w, r)
}

func dashboardRangeToSince(r string) time.Time {
	now := time.Now()
	switch r {
	case "1d":
		return now.AddDate(0, 0, -1)
	case "30d":
		return now.AddDate(0, -1, 0)
	case "90d":
		return now.AddDate(0, -3, 0)
	case "180d":
		return now.AddDate(0, -6, 0)
	case "365d":
		return now.AddDate(-1, 0, 0)
	default: // "7d"
		return now.AddDate(0, 0, -7)
	}
}

func intPtr(n int) *int { return &n }

// aggregateCostRows fetches the PAYG cost sub-totals for a dimension, logging and
// returning nil on error so a failed chart never blocks the whole page.
func (h *Handler) aggregateCostRows(ctx context.Context, filter port.UsageFilter, dim port.UsageDimension) []port.DimensionCost {
	rows, err := h.usageStore.AggregateCostByDimension(ctx, filter, dim)
	if err != nil {
		slog.WarnContext(ctx, "could not aggregate usage cost", slog.String("dimension", string(dim)), slogx.Error(err))
		return nil
	}
	return rows
}

// computeEnergyTotals iterates every record in the period to sum the (non-linear)
// energy estimate — the one figure that cannot be aggregated in SQL. Providers and
// models are resolved through the provider store cache.
func (h *Handler) computeEnergyTotals(ctx context.Context, filter port.UsageFilter) (totalEnergyWh, totalCO2GramsMid float64) {
	records, err := h.usageStore.QueryUsage(ctx, filter)
	if err != nil {
		slog.WarnContext(ctx, "could not query usage records for energy totals", slogx.Error(err))
		return 0, 0
	}
	modelCache := make(map[model.LLMModelID]model.LLMModel)
	providerCache := make(map[model.ProviderID]model.Provider)
	for _, rec := range records {
		mid := rec.ModelID()
		if mid == "" {
			continue
		}
		m, ok := modelCache[mid]
		if !ok {
			loaded, err := h.providerStore.GetLLMModelByID(ctx, mid)
			if err != nil {
				continue
			}
			m = loaded
			modelCache[mid] = m
		}
		if m.ActiveParams() <= 0 {
			continue
		}
		tier := estimator.TierHyperscaler
		pid := m.ProviderID()
		p, ok := providerCache[pid]
		if !ok {
			if loaded, err := h.providerStore.GetProviderByID(ctx, pid); err == nil {
				p = loaded
				providerCache[pid] = loaded
				ok = true
			}
		}
		if ok {
			tier = estimator.CloudTier(p.CloudTier())
		}
		est := estimator.NewCloudEstimator(tier).EstimateFromParams(
			float64(m.ActiveParams()),
			estimator.InferenceRequest{
				InputTokens:  int(rec.PromptTokens()),
				OutputTokens: int(rec.CompletionTokens()),
			},
			m.TokensPerSecLow(),
			m.TokensPerSecHigh(),
		)
		totalEnergyWh += est.Mid.TotalWh
		totalCO2GramsMid += est.Mid.Equivalences.CO2Grams
	}
	return totalEnergyWh, totalCO2GramsMid
}

func dashChartByValue(m map[string]int64) []component.ProfileChartDataPoint {
	pts := make([]component.ProfileChartDataPoint, 0, len(m))
	for label, cost := range m {
		pts = append(pts, component.ProfileChartDataPoint{Label: label, Value: float64(cost) / 1_000_000})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Value > pts[j].Value })
	return pts
}

func dashChartByProvider(m map[model.ProviderID]int64, names map[model.ProviderID]string) []component.ProfileChartDataPoint {
	pts := make([]component.ProfileChartDataPoint, 0, len(m))
	for pid, cost := range m {
		label := names[pid]
		if label == "" {
			label = string(pid)
		}
		pts = append(pts, component.ProfileChartDataPoint{Label: label, Value: float64(cost) / 1_000_000})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Value > pts[j].Value })
	return pts
}

// sumCostConverted returns the total cost for a user+org since the given time,
// converting each currency's sub-total to targetCurrency using the exchange
// rate service. Falls back to the raw amount when conversion is unavailable.
func (h *Handler) sumCostConverted(ctx context.Context, userID model.UserID, orgID model.OrgID, targetCurrency string, since time.Time) (int64, error) {
	byCurrency, err := h.usageStore.SumCostSinceByCurrency(ctx, []model.UserID{userID}, orgID, since)
	if err != nil {
		return 0, err
	}
	var total int64
	for cur, amount := range byCurrency {
		converted, convErr := h.exchangeRateService.Convert(ctx, amount, cur, targetCurrency)
		if convErr != nil {
			slog.WarnContext(ctx, "could not convert currency for display, using raw amount",
				slog.String("from", cur), slog.String("to", targetCurrency), slogx.Error(convErr))
			total += amount
		} else {
			total += converted
		}
	}
	return total, nil
}

func (h *Handler) serveUserUsageCSV(w http.ResponseWriter, r *http.Request, userID model.UserID, since time.Time) {
	ctx := r.Context()

	records, err := h.usageStore.QueryUsage(ctx, port.UsageFilter{
		UserID: &userID,
		Since:  &since,
	})
	if err != nil {
		slog.ErrorContext(ctx, "could not query usage records for CSV", slogx.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	memberships := httpCtx.Memberships(ctx)
	orgs := make(map[model.OrgID]model.Organization)
	for _, m := range memberships {
		if m.Org() != nil {
			orgs[m.OrgID()] = m.Org()
		}
	}

	modelCache := make(map[model.LLMModelID]model.LLMModel)
	providerCache := make(map[model.ProviderID]model.Provider)
	for _, rec := range records {
		mid := rec.ModelID()
		if mid == "" {
			continue
		}
		if _, ok := modelCache[mid]; ok {
			continue
		}
		m, err := h.providerStore.GetLLMModelByID(ctx, mid)
		if err != nil {
			continue
		}
		modelCache[mid] = m
		pid := m.ProviderID()
		if _, ok := providerCache[pid]; !ok {
			p, err := h.providerStore.GetProviderByID(ctx, pid)
			if err == nil {
				providerCache[pid] = p
			}
		}
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"usage-%s.csv\"", time.Now().Format("2006-01-02")))

	writer := csv.NewWriter(w)
	defer writer.Flush()

	writer.Write([]string{"Organisation", "Modèle", "Tokens prompt", "Tokens completion", "Coût", "Devise", "Énergie (Wh)", "CO₂ (g)", "Date"})

	for _, rec := range records {
		orgName := string(rec.OrgID())
		if org, ok := orgs[rec.OrgID()]; ok {
			orgName = org.Name()
		}

		modelName := rec.ProxyModelName()
		if rec.ResolvedModelName() != "" && rec.ResolvedModelName() != rec.ProxyModelName() {
			modelName = rec.ProxyModelName() + " → " + rec.ResolvedModelName()
		}

		var energyWh, co2Grams float64
		if m, ok := modelCache[rec.ModelID()]; ok && m.ActiveParams() > 0 {
			tier := estimator.TierHyperscaler
			if p, ok := providerCache[m.ProviderID()]; ok {
				tier = estimator.CloudTier(p.CloudTier())
			}
			est := estimator.NewCloudEstimator(tier).EstimateFromParams(
				float64(m.ActiveParams()),
				estimator.InferenceRequest{
					InputTokens:  int(rec.PromptTokens()),
					OutputTokens: int(rec.CompletionTokens()),
				},
				m.TokensPerSecLow(),
				m.TokensPerSecHigh(),
			)
			energyWh = est.Mid.TotalWh
			co2Grams = est.Mid.Equivalences.CO2Grams
		}

		cost := float64(rec.Cost()) / 1_000_000

		writer.Write([]string{
			orgName,
			modelName,
			strconv.Itoa(rec.PromptTokens()),
			strconv.Itoa(rec.CompletionTokens()),
			fmt.Sprintf("%.6f", cost),
			rec.Currency(),
			fmt.Sprintf("%.6f", energyWh),
			fmt.Sprintf("%.6f", co2Grams),
			rec.CreatedAt().Format("2006-01-02 15:04:05"),
		})
	}
}

// buildDashboardSubscriptionUsage builds subscription plan consumption for one org,
// scoped to a single user's personal fair-share: budgets and concurrency limits are
// divided by the org member count, and usage figures reflect only this user. Window
// timing (reset countdowns) is a window-level property and stays org-wide.
func (h *Handler) buildDashboardSubscriptionUsage(ctx context.Context, orgID model.OrgID, userID model.UserID) []orgcomponent.SubscriptionProviderUsage {
	providers, err := h.providerStore.ListProviders(ctx, orgID)
	if err != nil {
		slog.WarnContext(ctx, "could not list providers for subscription usage", slogx.Error(err))
		return nil
	}

	// Member count for fair-share division (mirrors the subscription enforcer).
	memberCount := int64(0)
	if _, count, err := h.orgStore.ListOrgMembers(ctx, orgID, port.ListOrgMembersOptions{}); err != nil {
		slog.WarnContext(ctx, "could not count org members for fair-share usage", slogx.Error(err))
	} else {
		memberCount = count
	}

	now := time.Now()
	var result []orgcomponent.SubscriptionProviderUsage

	for _, p := range providers {
		if p.BillingMode() != model.BillingModeSubscription {
			continue
		}
		plan := p.SubscriptionPlan()
		if plan == nil {
			continue
		}

		pu := orgcomponent.SubscriptionProviderUsage{
			Provider:    p,
			Plan:        *plan,
			Constraints: make([]orgcomponent.SubscriptionConstraintUsage, 0, len(plan.Constraints)),
			PerUser:     true,
		}

		for _, c := range plan.Constraints {
			// Fall back to the static share; rolling windows below replace it with
			// the allowance the enforcer actually grants right now.
			cu := orgcomponent.SubscriptionConstraintUsage{Constraint: fairShareConstraint(c, memberCount)}

			switch c.Kind {
			case model.ConstraintRollingWindow:
				dur := c.Duration.Duration()
				if dur > 0 {
					since := c.CurrentWindowStart(now)
					cu.WindowStart = since
					cu.Anchored = c.IsAnchored()
					cu.ResetAt = c.NextResetAt(now)
					tokens, value, sumErr := h.usageStore.SumUserPlanUsageSince(ctx, userID, orgID, p.ID(), since)
					if sumErr != nil {
						slog.WarnContext(ctx, "could not sum user plan usage", slogx.Error(sumErr))
					} else {
						cu.TokensUsed = tokens
						cu.ValueUsed = value
					}
					// Show the same denominators the enforcer decides against, so a
					// varying allowance stays readable instead of looking arbitrary.
					cu.Constraint, cu.FairShareMode = h.dashboardFairShare(ctx, dashboardFairShareArgs{
						Constraint:  c,
						OrgID:       orgID,
						ProviderID:  p.ID(),
						Since:       since,
						Now:         now,
						MemberCount: memberCount,
						UserActive:  cu.TokensUsed > 0 || cu.ValueUsed > 0,
					})
					// Window free-up hint is a window-level property → org-wide oldest record.
					if oldest, oldestErr := h.usageStore.EarliestPlanUsageSince(ctx, orgID, p.ID(), since); oldestErr != nil {
						slog.WarnContext(ctx, "could not get earliest plan usage", slogx.Error(oldestErr))
					} else {
						cu.OldestUsage = oldest
					}
				}

			case model.ConstraintConcurrency:
				if h.subscriptionMonitor != nil {
					cu.InFlight = h.subscriptionMonitor.CurrentUserInFlight(orgID, p.ID(), userID)
					if c.MaxConcurrent != nil {
						cu.Exhausted = h.subscriptionMonitor.IsExhausted(orgID, p.ID(), c.Label)
					}
				}
			}

			pu.Constraints = append(pu.Constraints, cu)
		}

		result = append(result, pu)
	}

	return result
}

// dashboardFairShareArgs carries the state a fair-share denominator is computed from.
type dashboardFairShareArgs struct {
	Constraint  model.PlanConstraint
	OrgID       model.OrgID
	ProviderID  model.ProviderID
	Since       time.Time
	Now         time.Time
	MemberCount int64
	// UserActive reports whether the viewer already consumed in this window; the
	// enforcer counts a first-time caller among the active users, so the displayed
	// share must do the same or it would overstate what they get.
	UserActive bool
}

// dashboardFairShare returns a copy of a rolling-window constraint whose budgets are
// the allowance model.ComputeFairShare grants the viewer right now, along with the
// mode that produced it. It mirrors rollingWindowEvaluator: any failure degrades to
// the static budget/members share rather than to a wider allowance.
func (h *Handler) dashboardFairShare(ctx context.Context, a dashboardFairShareArgs) (model.PlanConstraint, model.FairShareMode) {
	c := a.Constraint
	if a.MemberCount <= 0 || (c.TokenBudget == nil && c.ValueBudget == nil) {
		return c, ""
	}

	tokens, value, err := h.usageStore.SumPlanUsageSince(ctx, a.OrgID, a.ProviderID, a.Since)
	if err != nil {
		slog.WarnContext(ctx, "could not sum org plan usage for fair-share denominators", slogx.Error(err))
		return fairShareConstraint(c, a.MemberCount), ""
	}

	active, err := h.usageStore.CountActivePlanUsersSince(ctx, a.OrgID, a.ProviderID, a.Since)
	if err != nil {
		slog.WarnContext(ctx, "could not count active plan users for fair-share denominators", slogx.Error(err))
		active = a.MemberCount
	}
	if !a.UserActive {
		active++
	}

	base := model.FairShareParams{
		TotalMembers: int(a.MemberCount),
		ActiveUsers:  int(active),
		Elapsed:      c.ElapsedFraction(a.Now),
	}

	var mode model.FairShareMode
	if c.TokenBudget != nil {
		p := c.FairShareParamsFor(base)
		p.Budget, p.UsedTotal = *c.TokenBudget, tokens
		alloc := model.ComputeFairShare(p)
		c.TokenBudget = &alloc.Allowance
		mode = alloc.Mode
	}
	if c.ValueBudget != nil {
		p := c.FairShareParamsFor(base)
		p.Budget, p.UsedTotal = *c.ValueBudget, value
		alloc := model.ComputeFairShare(p)
		c.ValueBudget = &alloc.Allowance
		mode = alloc.Mode
	}

	return c, mode
}

// fairShareConstraint returns a copy of the constraint whose budgets (token, value,
// concurrency) are divided by the member count, matching the per-user fair-share the
// enforcer applies. When the member count is not positive, the constraint is unchanged.
func fairShareConstraint(c model.PlanConstraint, memberCount int64) model.PlanConstraint {
	if memberCount <= 1 {
		return c
	}
	if c.TokenBudget != nil {
		v := max(*c.TokenBudget/memberCount, 1)
		c.TokenBudget = &v
	}
	if c.ValueBudget != nil {
		v := max(*c.ValueBudget/memberCount, 1)
		c.ValueBudget = &v
	}
	if c.MaxConcurrent != nil {
		v := max(*c.MaxConcurrent/int(memberCount), 1)
		c.MaxConcurrent = &v
	}
	return c
}
