package org

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/adapter/cache"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/estimator"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

const usagePageSize = 20

func (h *Handler) getUsagePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	scope, ok := h.usageScope(w, r, org)
	if !ok {
		return
	}

	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			page = n
		}
	}
	offset := (page - 1) * usagePageSize

	rangeParam := r.URL.Query().Get("range")
	period, since := webcommon.UsageRange(rangeParam, time.Now())
	orgID := org.ID()

	// Support comma-separated owner values (users + applications) via 'owner' param
	rawOwnerFilter := r.URL.Query()["owner"]
	var ownerFilter []string
	for _, v := range rawOwnerFilter {
		for _, part := range strings.Split(v, ",") {
			if part != "" {
				ownerFilter = append(ownerFilter, part)
			}
		}
	}

	// CSV export
	if r.URL.Query().Get("format") == "csv" {
		h.serveOrgUsageCSV(w, r, org, scope, ownerFilter, since)
		return
	}

	// Fetch org members for filter
	members, _, err := h.orgStore.ListOrgMembers(ctx, org.ID(), port.ListOrgMembersOptions{})
	if err != nil {
		slog.WarnContext(ctx, "could not list org members", slogx.Error(err))
		members = nil
	}

	// Fetch applications for filter
	applications, err := h.applicationStore.QueryApplications(ctx, org.ID())
	if err != nil {
		slog.WarnContext(ctx, "could not list applications", slogx.Error(err))
		applications = nil
	}
	members, applications = scope.restrict(members, applications)

	// Build maps for filtering
	applicationsMap := make(map[model.ApplicationID]model.Application)
	for _, app := range applications {
		applicationsMap[app.ID()] = app
	}
	userMap := make(map[model.UserID]model.User)
	for _, m := range members {
		if m.User() != nil {
			userMap[m.User().ID()] = m.User()
		}
	}

	orgCurrency := org.Currency()

	// Separate owner filter into users and applications
	var userIDs []model.UserID
	var appIDs []model.ApplicationID
	for _, oid := range ownerFilter {
		if _, isApp := applicationsMap[model.ApplicationID(oid)]; isApp {
			appIDs = append(appIDs, model.ApplicationID(oid))
		} else if _, isUser := userMap[model.UserID(oid)]; isUser {
			userIDs = append(userIDs, model.UserID(oid))
		}
	}

	usageFilter := port.UsageFilter{
		OrgID:          &orgID,
		TeamID:         scope.filter(),
		Since:          &since,
		ApplicationIDs: appIDs,
	}
	if len(userIDs) > 0 {
		usageFilter.UserIDs = userIDs
	}

	// Aggregate without currency filter so all records are counted
	agg, err := h.usageStore.AggregateUsage(ctx, usageFilter)
	if err != nil {
		slog.ErrorContext(ctx, "could not aggregate usage", slogx.Error(err))
		agg = &port.UsageAggregate{}
	}

	// Compute total cost with per-currency conversion. With a team filter, the
	// total is the sum of the daily chart below, which is filtered too.
	if agg != nil && scope.team == nil {
		byCurrency, sumErr := h.usageStore.SumCostSinceByCurrency(ctx, userIDs, orgID, since)
		if sumErr != nil {
			slog.ErrorContext(ctx, "could not sum cost by currency", slogx.Error(sumErr))
		} else {
			var totalConverted int64
			for cur, amount := range byCurrency {
				converted, convErr := h.exchangeRateService.Convert(ctx, amount, cur, orgCurrency)
				if convErr != nil {
					slog.WarnContext(ctx, "currency conversion failed, using raw amount",
						slog.String("from", cur), slog.String("to", orgCurrency), slogx.Error(convErr))
					totalConverted += amount
				} else {
					totalConverted += converted
				}
			}
			agg.TotalCost = totalConverted
			agg.Currency = orgCurrency
		}
	}

	// Fetch one extra record to detect whether a next page exists
	rawRecords, err := h.usageStore.QueryUsage(ctx, port.UsageFilter{
		OrgID:          &orgID,
		TeamID:         scope.filter(),
		Since:          &since,
		Limit:          intPtr(usagePageSize + 1),
		Offset:         intPtr(offset),
		UserIDs:        userIDs,
		ApplicationIDs: appIDs,
	})
	if err != nil {
		slog.ErrorContext(ctx, "could not query usage records", slogx.Error(err))
		rawRecords = nil
	}

	hasNext := len(rawRecords) > usagePageSize
	if hasNext {
		rawRecords = rawRecords[:usagePageSize]
	}

	// Batch-load users for the records on this page
	users := make(map[model.UserID]model.User)
	for _, rec := range rawRecords {
		uid := rec.UserID()
		if _, ok := users[uid]; ok {
			continue
		}
		u, err := h.userStore.GetUserByID(ctx, uid)
		if err != nil {
			slog.WarnContext(ctx, "could not fetch user for usage record", slogx.Error(err), slog.String("userID", string(uid)))
			continue
		}
		users[uid] = u
	}

	// Batch-load applications for the records on this page
	applicationsMap = make(map[model.ApplicationID]model.Application)
	for _, rec := range rawRecords {
		aid := rec.ApplicationID()
		if aid == "" {
			continue
		}
		if _, ok := applicationsMap[aid]; ok {
			continue
		}
		a, err := h.applicationStore.GetApplication(ctx, aid)
		if err != nil {
			slog.WarnContext(ctx, "could not fetch application for usage record", slogx.Error(err), slog.String("appID", string(aid)))
			continue
		}
		applicationsMap[aid] = a
	}

	// Cache key for energy totals (includes user and app filter for uniqueness). It
	// names the range, not its start: since moves with the clock, and a key built
	// from it never hit the cache. The owners are sorted and deduplicated: their
	// order and repetition in the query string select nothing.
	energyCacheKey := fmt.Sprintf("org-usage:%s:%s:%v:%v:%s", orgID, period, slices.Compact(slices.Sorted(slices.Values(userIDs))), slices.Compact(slices.Sorted(slices.Values(appIDs))), scope.teamID())

	// Filter matching the whole selected period, used by every chart aggregation.
	chartFilter := port.UsageFilter{
		OrgID:          &orgID,
		TeamID:         scope.filter(),
		Since:          &since,
		UserIDs:        userIDs,
		ApplicationIDs: appIDs,
	}

	// Build the model/provider caches for the *paginated* records only (bounded by the
	// page size, served by the provider store cache). Used for the per-row energy column.
	pageModelCache := make(map[model.LLMModelID]model.LLMModel)
	pageProviderCache := make(map[model.ProviderID]model.Provider)
	for _, rec := range rawRecords {
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
	// period into memory. All records belong to this org so each sub-total is converted
	// from its stored currency to the org currency.
	//
	// The sub-totals include the requests covered by a subscription, at their equivalent
	// PAYG value: an org served entirely by a plan would otherwise face empty charts over
	// a list of requests. The daily chart stacks the two apart; the model and provider
	// breakdowns add them up; the leaderboard keeps to billed spend, as it announces.
	convertToOrg := func(rows []port.DimensionCost) map[string]int64 {
		out := make(map[string]int64, len(rows))
		for _, row := range rows {
			cost := row.Cost
			if row.Currency != orgCurrency {
				if converted, convErr := h.exchangeRateService.Convert(ctx, row.Cost, row.Currency, orgCurrency); convErr == nil {
					cost = converted
				}
			}
			out[row.Key] += cost
		}
		return out
	}

	perModel := convertToOrg(h.aggregateCostRows(ctx, chartFilter, port.UsageDimensionModel))

	paygDayRows, coveredDayRows := port.SplitPlanCovered(h.aggregateCostRows(ctx, chartFilter, port.UsageDimensionDay))
	perDay := convertToOrg(paygDayRows)
	coveredPerDay := convertToOrg(coveredDayRows)
	if agg != nil && scope.team != nil {
		var total int64
		for _, cost := range perDay {
			total += cost
		}
		agg.TotalCost, agg.Currency = total, orgCurrency
	}

	// Per-user PAYG cost, keyed by display name (two ids sharing a name merge, as before).
	// The subscription consumption is charted apart, in tokens, below.
	paygUserRows, _ := port.SplitPlanCovered(h.aggregateCostRows(ctx, chartFilter, port.UsageDimensionUser))
	perUser := make(map[string]int64)
	for key, cost := range convertToOrg(paygUserRows) {
		perUser[h.usageUserLabel(ctx, model.UserID(key), userMap)] += cost
	}

	// Per-provider cost, keyed by provider id.
	providerRows := h.aggregateCostRows(ctx, chartFilter, port.UsageDimensionProvider)
	perProvider := make(map[model.ProviderID]int64)
	for key, cost := range convertToOrg(providerRows) {
		perProvider[model.ProviderID(key)] += cost
	}
	coveredProviders := make(map[model.ProviderID]bool)
	for _, row := range providerRows {
		if row.PlanCovered {
			coveredProviders[model.ProviderID(row.Key)] = true
		}
	}

	// Consommation en tokens des requêtes couvertes par un abonnement, par utilisateur.
	// Les requêtes couvertes par un abonnement ont un coût forfaitaire fixe : leur coût
	// par token est une estimation fictive qui gonflerait artificiellement le classement
	// des consommateurs (les cartes de coût total et le classement ci-dessus excluent
	// plan_covered). On les comptabilise à part, en volume de tokens consommés.
	subTokensPerUser := make(map[string]int64)
	if planRows, err := h.usageStore.AggregatePlanTokensByUser(ctx, chartFilter); err != nil {
		slog.WarnContext(ctx, "could not aggregate plan tokens by user", slogx.Error(err))
	} else {
		for _, row := range planRows {
			subTokensPerUser[h.usageUserLabel(ctx, row.UserID, userMap)] += row.Tokens
		}
	}

	// Energy is non-linear per request: it is estimated from the requests grouped by
	// model and token counts, and cached for a few minutes.
	var totalEnergyWh, totalCO2GramsMid float64
	if cached, ok := cache.EnergyEstimateCache.Get(energyCacheKey); ok {
		totalEnergyWh = cached.TotalEnergyWh
		totalCO2GramsMid = cached.TotalCO2GramsMid
	} else {
		var computed bool
		totalEnergyWh, totalCO2GramsMid, computed = h.computeEnergyTotals(ctx, chartFilter)
		if computed {
			cache.EnergyEstimateCache.Add(energyCacheKey, cache.EnergyTotals{
				TotalEnergyWh:    totalEnergyWh,
				TotalCO2GramsMid: totalCO2GramsMid,
			})
		}
	}

	// Build display records with cost converted to org currency where needed
	records := make([]component.OrgDisplayUsageRecord, 0, len(rawRecords))
	for _, rec := range rawRecords {
		displayModelName := rec.ProxyModelName()
		if rec.ResolvedModelName() != "" && rec.ResolvedModelName() != rec.ProxyModelName() {
			displayModelName = rec.ProxyModelName() + " → " + rec.ResolvedModelName()
		}
		dr := component.OrgDisplayUsageRecord{
			Record:           rec,
			DisplayModelName: displayModelName,
			DisplayCost:      rec.Cost(),
			DisplayCurrency:  rec.Currency(),
		}
		if orgCurrency != rec.Currency() {
			converted, convErr := h.exchangeRateService.Convert(ctx, rec.Cost(), rec.Currency(), orgCurrency)
			if convErr == nil {
				dr.DisplayCost = converted
				dr.DisplayCurrency = orgCurrency
				dr.Converted = true
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
		records = append(records, dr)
	}

	// Fetch the budget of the card: the org's, or the filtered team's.
	var orgQuota model.Quota
	var dailyCost, monthlyCost, yearlyCost int64
	now := time.Now()
	budgetTitle, budgetHref := "", ""
	if scope.team != nil {
		if q, err := h.optionalQuota(ctx, model.QuotaScopeTeam, string(scope.team.ID())); err == nil {
			orgQuota = q
		}
		if spend, err := h.scopeSpend(ctx, model.QuotaScopeTeam, string(scope.team.ID()), orgID, now); err == nil {
			dailyCost, monthlyCost, yearlyCost = spend[0], spend[1], spend[2]
		}
		budgetTitle = i18n.T(ctx, "org.nav.usage_budget_team", scope.team.Name())
		budgetHref = teamURL(org, scope.team)
	} else {
		if q, err := h.quotaStore.GetQuota(ctx, model.QuotaScopeOrg, string(orgID)); err == nil && q != nil {
			orgQuota = q
		}

		// Compute daily/monthly/yearly spending for quota pie charts
		dailyCost = h.sumConvertedCost(ctx, nil, orgID, startOfPeriod("day", now), orgCurrency)
		monthlyCost = h.sumConvertedCost(ctx, nil, orgID, startOfPeriod("month", now), orgCurrency)
		yearlyCost = h.sumConvertedCost(ctx, nil, orgID, startOfPeriod("year", now), orgCurrency)
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

	// Build subscription provider consumption data.
	subscriptionProviders := h.buildSubscriptionProviderUsage(ctx, orgID)

	costPerDay := common.StackedCostSeries(r.Context(), []map[string]int64{perDay, coveredPerDay}, since, time.Now(), rangeParam)

	vmodel := component.OrgUsagePageVModel{
		Org:                   org,
		Aggregate:             agg,
		Records:               records,
		Users:                 users,
		Members:               members,
		OwnerFilter:           ownerFilter,
		Teams:                 scope.teams,
		TeamFilter:            scope.teamID(),
		TeamOnly:              scope.teamOnly,
		BudgetTitle:           budgetTitle,
		BudgetHref:            budgetHref,
		Applications:          applications,
		ApplicationsMap:       applicationsMap,
		Since:                 since,
		Range:                 rangeParam,
		Page:                  page,
		PageSize:              usagePageSize,
		HasNext:               hasNext,
		SubscriptionProviders: subscriptionProviders,
		OrgQuota:              orgQuota,
		DailyCost:             dailyCost,
		MonthlyCost:           monthlyCost,
		YearlyCost:            yearlyCost,
		Currency:              orgCurrency,
		ChartPerDay:           costPerDay[0],
		ChartCoveredPerDay:    costPerDay[1],
		ChartSharesPerModel:   common.ChartShares(common.TopNChartDataPoints(r.Context(), chartByValue(perModel), 5)),
		ChartPerUser:          chartByValue(perUser),
		ChartPerProvider:      chartByProvider(perProvider, providerNames),
		PlanCoveredProviders:  common.PlanCoveredLabels(coveredProviders, providerNames),
		ChartSubTokensPerUser: chartTokensByValue(subTokensPerUser),
		TotalEnergyWh:         totalEnergyWh,
		TotalCO2GramsMid:      totalCO2GramsMid,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-usage",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: i18n.T(ctx, "org.nav.dashboard"), Href: ""},
			},
		},
	}

	templ.Handler(component.OrgUsagePage(vmodel)).ServeHTTP(w, r)
}

func intPtr(n int) *int { return &n }

// aggregateCostRows fetches the cost sub-totals for a dimension, logging and
// returning nil on error so a failed chart never blocks the whole page.
func (h *Handler) aggregateCostRows(ctx context.Context, filter port.UsageFilter, dim port.UsageDimension) []port.DimensionCost {
	rows, err := h.usageStore.AggregateCostByDimension(ctx, filter, dim)
	if err != nil {
		slog.WarnContext(ctx, "could not aggregate usage cost", slog.String("dimension", string(dim)), slogx.Error(err))
		return nil
	}
	return rows
}

// usageUserLabel resolves a user id to a display name, preferring the already-loaded
// org members map and falling back to a (cached) store lookup, then to the raw id.
func (h *Handler) usageUserLabel(ctx context.Context, uid model.UserID, userMap map[model.UserID]model.User) string {
	if u, ok := userMap[uid]; ok && u != nil {
		return u.DisplayName()
	}
	if u, err := h.userStore.GetUserByID(ctx, uid); err == nil {
		return u.DisplayName()
	}
	return string(uid)
}

// computeEnergyTotals estimates the energy and CO₂ of the usage matching the
// filter. On error it logs and reports false: the page shows zero and the
// caller does not cache it.
func (h *Handler) computeEnergyTotals(ctx context.Context, filter port.UsageFilter) (totalEnergyWh, totalCO2GramsMid float64, ok bool) {
	totalEnergyWh, totalCO2GramsMid, err := service.EstimateUsageEnergy(ctx, h.usageStore, h.providerStore, filter)
	if err != nil {
		slog.WarnContext(ctx, "could not estimate usage energy", slogx.Error(err))
		return 0, 0, false
	}
	return totalEnergyWh, totalCO2GramsMid, true
}

// sumConvertedCost sums costs from the given time, converting each currency to targetCurrency.
// If userIDs is non-empty, only costs for those users are summed; otherwise the whole org is summed.
//
// Errors from the store or the currency conversion are logged at error level
// and the function returns 0. Callers that need to surface the failure to the
// operator should use loadOrgSpend instead, which propagates the error so
// the page can render a "Consommation indisponible" banner alongside the
// displayed gauge.
func (h *Handler) sumConvertedCost(ctx context.Context, userIDs []model.UserID, orgID model.OrgID, since time.Time, targetCurrency string) int64 {
	total, err := h.loadOrgSpend(ctx, userIDs, orgID, since, targetCurrency)
	if err != nil {
		slog.ErrorContext(ctx, "could not load org spend for quota display", slogx.Error(err))
		return 0
	}
	return total
}

// loadOrgSpend is the error-returning variant of sumConvertedCost. Quota
// pages use it to populate the "Consommation indisponible" banner when the
// underlying lookup fails, instead of silently rendering a misleading 0%.
func (h *Handler) loadOrgSpend(ctx context.Context, userIDs []model.UserID, orgID model.OrgID, since time.Time, targetCurrency string) (int64, error) {
	byCurrency, err := h.usageStore.SumCostSinceByCurrency(ctx, userIDs, orgID, since)
	if err != nil {
		return 0, errors.WithStack(err)
	}
	var total int64
	for cur, amount := range byCurrency {
		converted, err := h.exchangeRateService.Convert(ctx, amount, cur, targetCurrency)
		if err != nil {
			// Currency conversion errors are usually transient (missing rate).
			// Fall back to the raw microcent amount and keep going: the gauge
			// under-reports slightly on a transient outage rather than going
			// blank. A dedicated diagnostic above the chart surfaces the gap.
			slog.WarnContext(ctx, "currency conversion failed; falling back to raw microcents",
				slog.String("currency", cur), slog.String("target", targetCurrency), slogx.Error(err))
			total += amount
			continue
		}
		total += converted
	}
	return total, nil
}

func startOfPeriod(period string, t time.Time) time.Time {
	y, m, d := t.Date()
	switch period {
	case "day":
		return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
	case "month":
		return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
	default: // year
		return time.Date(y, 1, 1, 0, 0, 0, 0, t.Location())
	}
}

func chartByValue(m map[string]int64) []component.ChartDataPoint {
	pts := make([]component.ChartDataPoint, 0, len(m))
	for label, cost := range m {
		pts = append(pts, component.ChartDataPoint{Label: label, Value: float64(cost) / 1_000_000})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Value > pts[j].Value })
	return pts
}

// chartTokensByValue builds chart points from a per-label token count. Unlike chartByValue,
// values are raw token counts (not divided into a currency amount).
func chartTokensByValue(m map[string]int64) []component.ChartDataPoint {
	pts := make([]component.ChartDataPoint, 0, len(m))
	for label, tokens := range m {
		pts = append(pts, component.ChartDataPoint{Label: label, Value: float64(tokens)})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Value > pts[j].Value })
	return pts
}

func chartByProvider(m map[model.ProviderID]int64, names map[model.ProviderID]string) []component.ChartDataPoint {
	pts := make([]component.ChartDataPoint, 0, len(m))
	for pid, cost := range m {
		label := names[pid]
		if label == "" {
			label = string(pid)
		}
		pts = append(pts, component.ChartDataPoint{Label: label, Value: float64(cost) / 1_000_000})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Value > pts[j].Value })
	return pts
}

func (h *Handler) serveOrgUsageCSV(w http.ResponseWriter, r *http.Request, org model.Organization, scope usageScope, ownerFilter []string, since time.Time) {
	ctx := r.Context()
	orgID := org.ID()

	members, _, err := h.orgStore.ListOrgMembers(ctx, org.ID(), port.ListOrgMembersOptions{})
	if err != nil {
		slog.WarnContext(ctx, "could not list org members for CSV", slogx.Error(err))
	}

	applications, err := h.applicationStore.QueryApplications(ctx, org.ID())
	if err != nil {
		slog.WarnContext(ctx, "could not list applications for CSV", slogx.Error(err))
	}
	members, applications = scope.restrict(members, applications)

	applicationsMap := make(map[model.ApplicationID]model.Application)
	for _, app := range applications {
		applicationsMap[app.ID()] = app
	}
	userMap := make(map[model.UserID]model.User)
	for _, m := range members {
		if m.User() != nil {
			userMap[m.User().ID()] = m.User()
		}
	}

	// Separate owner filter into users and applications
	var userIDs []model.UserID
	var applicationIDs []model.ApplicationID
	for _, oid := range ownerFilter {
		if _, isApp := applicationsMap[model.ApplicationID(oid)]; isApp {
			applicationIDs = append(applicationIDs, model.ApplicationID(oid))
		} else if _, isUser := userMap[model.UserID(oid)]; isUser {
			userIDs = append(userIDs, model.UserID(oid))
		}
	}

	usageFilter := port.UsageFilter{
		OrgID:  &orgID,
		TeamID: scope.filter(),
		Since:  &since,
	}
	if len(userIDs) > 0 {
		usageFilter.UserIDs = userIDs
	}
	if len(applicationIDs) > 0 {
		usageFilter.ApplicationIDs = applicationIDs
	}

	records, err := h.usageStore.QueryUsage(ctx, usageFilter)
	if err != nil {
		slog.ErrorContext(ctx, "could not query usage records for CSV", slogx.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	userNames := make(map[model.UserID]string)
	for _, m := range members {
		userNames[m.UserID()] = m.User().DisplayName()
	}

	appNames := make(map[model.ApplicationID]string)
	for _, app := range applications {
		appNames[app.ID()] = app.Name()
	}

	modelCache := make(map[model.LLMModelID]model.LLMModel)
	providerCache := make(map[model.ProviderID]model.Provider)
	for _, rec := range records {
		// Cache the provider by the record's provider id directly, so the name is available
		// even for records without a resolved model.
		if pid := rec.ProviderID(); pid != "" {
			if _, ok := providerCache[pid]; !ok {
				if p, err := h.providerStore.GetProviderByID(ctx, pid); err == nil {
					providerCache[pid] = p
				}
			}
		}
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
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-usage-%s.csv\"", org.Slug(), time.Now().Format("2006-01-02")))

	writer := csv.NewWriter(w)
	defer writer.Flush()

	writer.Write([]string{"Utilisateur", "Type", "Modèle", "Fournisseur", "Tokens prompt", "Tokens cache", "Tokens completion", "Coût", "Devise", "Énergie (Wh)", "CO₂ (g)", "Date"})

	for _, rec := range records {
		var ownerName string
		var ownerType string
		if appID := rec.ApplicationID(); appID != "" {
			ownerName = appNames[model.ApplicationID(appID)]
			if ownerName == "" {
				ownerName = string(appID)
			}
			ownerType = "application"
		} else {
			ownerName = userNames[rec.UserID()]
			if ownerName == "" {
				ownerName = string(rec.UserID())
			}
			ownerType = "user"
		}

		modelName := rec.ProxyModelName()
		if rec.ResolvedModelName() != "" && rec.ResolvedModelName() != rec.ProxyModelName() {
			modelName = rec.ProxyModelName() + " → " + rec.ResolvedModelName()
		}

		providerName := ""
		if p, ok := providerCache[rec.ProviderID()]; ok {
			providerName = p.Name()
		} else if rec.ProviderID() != "" {
			providerName = string(rec.ProviderID())
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
			ownerName,
			ownerType,
			modelName,
			providerName,
			strconv.Itoa(rec.PromptTokens()),
			strconv.Itoa(rec.CachedTokens()),
			strconv.Itoa(rec.CompletionTokens()),
			fmt.Sprintf("%.6f", cost),
			rec.Currency(),
			fmt.Sprintf("%.6f", energyWh),
			fmt.Sprintf("%.6f", co2Grams),
			rec.CreatedAt().Format("2006-01-02 15:04:05"),
		})
	}
}

// fillRollingWindowUsage reads the consumption of a rolling-window constraint
// on a plan scope: the whole provider, or one upstream model. totals, when
// set, is the window's consumption as the caller already read it.
func (h *Handler) fillRollingWindowUsage(ctx context.Context, cu *component.SubscriptionConstraintUsage, c model.PlanConstraint, scope model.PlanUsageScope, now time.Time, totals *port.PlanUsageTotals) {
	if c.Duration.Duration() <= 0 {
		return
	}
	since := c.CurrentWindowStart(now)
	cu.WindowStart = since
	cu.Anchored = c.IsAnchored()
	cu.ResetAt = c.NextResetAt(now)
	if totals != nil {
		cu.TokensUsed, cu.ValueUsed = totals.Tokens, totals.Value
	} else if tokens, value, err := h.usageStore.SumPlanUsageSince(ctx, scope, since); err != nil {
		slog.WarnContext(ctx, "could not sum plan usage", slogx.Error(err))
	} else {
		cu.TokensUsed = tokens
		cu.ValueUsed = value
	}
	if oldest, err := h.usageStore.EarliestPlanUsageSince(ctx, scope, since); err != nil {
		slog.WarnContext(ctx, "could not get earliest plan usage", slogx.Error(err))
	} else {
		cu.OldestUsage = oldest
	}
}

// perModelConstraintUsage expands a per-model constraint into one gauge per
// upstream model consumed in the current window. A plan metering a few dozen
// models would bury the page under idle gauges; when none is in use a single
// row says so.
func (h *Handler) perModelConstraintUsage(ctx context.Context, c model.PlanConstraint, providerScope model.PlanUsageScope, models []model.LLMModel, now time.Time) []component.SubscriptionConstraintUsage {
	idle := component.SubscriptionConstraintUsage{Constraint: c, Anchored: c.IsAnchored(), ResetAt: c.NextResetAt(now)}
	if c.Duration.Duration() <= 0 {
		return []component.SubscriptionConstraintUsage{idle}
	}

	byModel, err := h.usageStore.SumPlanUsageByModelSince(ctx, providerScope, "", c.CurrentWindowStart(now))
	if err != nil {
		slog.WarnContext(ctx, "could not sum per-model plan usage", slogx.Error(err))
		return []component.SubscriptionConstraintUsage{idle}
	}

	upstream := service.GroupPlanUsageByUpstreamModel(models, providerScope.ProviderID, byModel)
	if len(upstream) == 0 {
		return []component.SubscriptionConstraintUsage{idle}
	}

	result := make([]component.SubscriptionConstraintUsage, 0, len(upstream))
	for _, um := range upstream {
		mc := c.ForModel(um.UpstreamModel)
		cu := component.SubscriptionConstraintUsage{Constraint: mc, UpstreamModel: um.UpstreamModel}
		scope := providerScope
		scope.ModelIDs = um.ModelIDs
		h.fillRollingWindowUsage(ctx, &cu, mc, scope, now, &port.PlanUsageTotals{Tokens: um.Tokens, Value: um.Value})
		if h.subscriptionMonitor != nil {
			cu.Exhausted = h.subscriptionMonitor.IsExhausted(providerScope.OrgID, providerScope.ProviderID, c.Label, um.UpstreamModel)
		}
		result = append(result, cu)
	}
	return result
}

// buildSubscriptionProviderUsage collects runtime plan consumption for every
// subscription provider belonging to the org.
func (h *Handler) buildSubscriptionProviderUsage(ctx context.Context, orgID model.OrgID) []component.SubscriptionProviderUsage {
	providers, err := h.providerStore.ListProviders(ctx, orgID)
	if err != nil {
		slog.WarnContext(ctx, "could not list providers for subscription usage", slogx.Error(err))
		return nil
	}

	now := time.Now()
	var result []component.SubscriptionProviderUsage

	for _, p := range providers {
		if p.BillingMode() != model.BillingModeSubscription {
			continue
		}
		plan := p.SubscriptionPlan()
		if plan == nil {
			continue
		}

		pu := component.SubscriptionProviderUsage{
			Provider:    p,
			Plan:        *plan,
			Constraints: make([]component.SubscriptionConstraintUsage, 0, len(plan.Constraints)),
		}

		providerScope := model.PlanUsageScope{OrgID: orgID, ProviderID: p.ID()}
		var models []model.LLMModel
		if plan.HasPerModelConstraint() {
			if models, err = h.providerStore.ListLLMModels(ctx, orgID); err != nil {
				slog.WarnContext(ctx, "could not list models for per-model plan usage", slogx.Error(err))
			}
		}

		for _, c := range plan.Constraints {
			if c.PerModel() {
				pu.Constraints = append(pu.Constraints, h.perModelConstraintUsage(ctx, c, providerScope, models, now)...)
				continue
			}

			cu := component.SubscriptionConstraintUsage{Constraint: c}

			switch c.Kind {
			case model.ConstraintRollingWindow:
				h.fillRollingWindowUsage(ctx, &cu, c, providerScope, now, nil)

			case model.ConstraintConcurrency:
				if h.subscriptionMonitor != nil {
					cu.InFlight = h.subscriptionMonitor.CurrentInFlight(orgID, p.ID())
					if c.MaxConcurrent != nil {
						cu.Exhausted = h.subscriptionMonitor.IsExhausted(orgID, p.ID(), c.Label, "")
					}
				}
			}

			pu.Constraints = append(pu.Constraints, cu)
		}

		result = append(result, pu)
	}

	return result
}

// usageScope is what the usage page shows: the organization, or one team.
type usageScope struct {
	// teams are the teams the viewer can filter on.
	teams []model.Team
	// team is the selected team, nil for the whole organization.
	team model.Team
	// teamOnly is set when the viewer reads the usage of its teams only.
	teamOnly bool
	// teamMembers holds the users of the selected team.
	teamMembers map[model.UserID]bool
}

func (s usageScope) teamID() model.TeamID {
	if s.team == nil {
		return ""
	}
	return s.team.ID()
}

func (s usageScope) filter() *model.TeamID {
	if s.team == nil {
		return nil
	}
	id := s.team.ID()
	return &id
}

// restrict keeps, with a team selected, the members and applications of the
// team: the caller filter of a team view lists nobody else.
func (s usageScope) restrict(members []model.Membership, apps []model.Application) ([]model.Membership, []model.Application) {
	if s.team == nil {
		return members, apps
	}
	var keptMembers []model.Membership
	for _, m := range members {
		if s.teamMembers[m.UserID()] {
			keptMembers = append(keptMembers, m)
		}
	}
	var keptApps []model.Application
	for _, app := range apps {
		if app.TeamID() == s.team.ID() {
			keptApps = append(keptApps, app)
		}
	}
	return keptMembers, keptApps
}

// usageScope resolves what the usage page may show. usage:read on the
// organization shows everything, the "team" query parameter narrowing it to
// one team; usage:read held on teams only shows one of them, never the
// organization. It writes the response and returns false otherwise.
func (h *Handler) usageScope(w http.ResponseWriter, r *http.Request, org model.Organization) (usageScope, bool) {
	ctx := r.Context()
	perms, err := httpCtx.ResolvePermissions(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not resolve permissions", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return usageScope{}, false
	}
	var scope usageScope
	if h.teamStore != nil {
		teams, err := h.teamStore.ListOrgTeams(ctx, org.ID())
		if err != nil {
			slog.ErrorContext(ctx, "could not list teams", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return usageScope{}, false
		}
		for _, team := range teams {
			if perms.HasInTeam(string(team.ID()), rbac.PermUsageRead) {
				scope.teams = append(scope.teams, team)
			}
		}
	}
	scope.teamOnly = !perms.Has(rbac.PermUsageRead)
	if scope.teamOnly && len(scope.teams) == 0 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return usageScope{}, false
	}
	wanted := model.TeamID(r.URL.Query().Get("team"))
	for _, team := range scope.teams {
		if team.ID() == wanted {
			scope.team = team
		}
	}
	if scope.team == nil && scope.teamOnly {
		scope.team = scope.teams[0]
	}
	if scope.team != nil {
		members, err := h.teamStore.ListTeamMembers(ctx, scope.team.ID())
		if err != nil {
			slog.ErrorContext(ctx, "could not list team members", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return usageScope{}, false
		}
		scope.teamMembers = map[model.UserID]bool{}
		for _, m := range members {
			scope.teamMembers[m.UserID()] = true
		}
	}
	return scope, true
}
