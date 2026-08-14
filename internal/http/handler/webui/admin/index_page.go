package admin

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/admin/component"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/pkg/errors"
)

// overviewWindow is the period the platform overview reports on. The mockup
// pins it to 30 days; the 24 h / 7 j / 12 m segments of its header are lot 7.
const overviewWindow = 30 * 24 * time.Hour

// overviewChartDays caps the number of columns of the daily histogram so a wide
// window does not produce unreadable hairlines.
const overviewChartDays = 30

func (h *Handler) getIndexPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)

	since := time.Now().Add(-overviewWindow)

	tenantID := httpCtx.TenantID(ctx)

	orgs, _, err := h.orgStore.ListOrgs(ctx, port.ListOrgsOptions{TenantID: &tenantID})
	if err != nil {
		slog.ErrorContext(ctx, "could not list orgs", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Organisations do not have to share a currency, so every sub-total is
	// converted into a single reference before anything is summed or stacked.
	currency := model.DefaultCurrency

	rows := h.overviewOrgs(ctx, orgs, since, currency)

	vmodel := component.OverviewPageVModel{
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			IsAdmin:      true,
			SelectedItem: "overview",
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: "Plateforme", Href: "/admin/"},
				{Label: "Vue d'ensemble", Href: ""},
			},
			Context: common.ContextPlatform,
		},
		Currency:   currency,
		Since:      since,
		Orgs:       rows,
		CostSeries: h.overviewCostSeries(ctx, rows, since, currency),
		TotalOrgs:  len(orgs),
	}

	for _, row := range rows {
		vmodel.TotalCost += row.Cost
		vmodel.TotalTokens += row.Tokens
		vmodel.TotalRequests += row.Requests
		vmodel.TotalMembers += row.Members
	}

	templ.Handler(component.OverviewPage(vmodel)).ServeHTTP(w, r)
}

// overviewOrgs builds one row per organisation, sorted by descending cost so the
// table and the histogram legend agree on the order — and so the colour an
// organisation gets in the chart is stable within a render.
func (h *Handler) overviewOrgs(ctx context.Context, orgs []model.Organization, since time.Time, currency string) []component.OverviewOrg {
	rows := make([]component.OverviewOrg, 0, len(orgs))

	for _, org := range orgs {
		orgID := org.ID()
		row := component.OverviewOrg{
			ID:     orgID,
			Name:   org.Name(),
			Slug:   org.Slug(),
			Active: org.Active(),
		}

		if _, total, err := h.orgStore.ListOrgMembers(ctx, orgID, port.ListOrgMembersOptions{}); err != nil {
			slog.WarnContext(ctx, "could not count org members", slogx.Error(err), slog.String("orgID", string(orgID)))
		} else {
			row.Members = int(total)
		}

		agg, err := h.usageStore.AggregateUsage(ctx, port.UsageFilter{OrgID: &orgID, Since: &since})
		if err != nil {
			slog.WarnContext(ctx, "could not aggregate org usage", slogx.Error(err), slog.String("orgID", string(orgID)))
		} else if agg != nil {
			// AggregateUsage returns the cost in the org's own currency.
			row.Cost = h.convert(ctx, agg.TotalCost, agg.Currency, currency)
			row.Tokens = agg.TotalTokens
			row.Requests = agg.TotalRequests
		}

		rows = append(rows, row)
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Cost > rows[j].Cost })
	for i := range rows {
		rows[i].ColorClass = component.OverviewOrgColor(i)
	}

	return rows
}

// overviewCostSeries builds the stacked cost histogram: one bar per day of the
// window, one series per organisation.
//
// The sub-totals come from a single GROUP BY over the whole period rather than
// one query per organisation, then are converted and bucketed in memory.
//
// Only organisations that actually consumed pay-as-you-go end up in the result:
// AggregateCostByDimension excludes the requests covered by a subscription, so
// an organisation billed entirely on a plan has nothing to draw — and must not
// appear in the legend either, which is built from these series.
func (h *Handler) overviewCostSeries(ctx context.Context, orgs []component.OverviewOrg, since time.Time, currency string) component.OverviewCostSeries {
	rows, err := h.usageStore.AggregateCostByDimension(ctx, port.UsageFilter{Since: &since}, port.UsageDimensionDay)
	if err != nil {
		slog.WarnContext(ctx, "could not aggregate platform cost by day", slogx.Error(err))
		return component.OverviewCostSeries{}
	}
	if len(rows) == 0 {
		return component.OverviewCostSeries{}
	}

	names := make(map[model.OrgID]string, len(orgs))
	order := make(map[model.OrgID]int, len(orgs))
	for i, org := range orgs {
		names[org.ID] = org.Name
		order[org.ID] = i
	}

	// day -> org -> cost, in the reference currency.
	perDay := make(map[string]map[model.OrgID]int64)
	for _, row := range rows {
		day, ok := perDay[row.Key]
		if !ok {
			day = make(map[model.OrgID]int64)
			perDay[row.Key] = day
		}
		day[row.OrgID] += h.convert(ctx, row.Cost, row.Currency, currency)
	}

	days := make([]string, 0, len(perDay))
	for day := range perDay {
		days = append(days, day)
	}
	// The dimension key is an ISO date, so lexicographic order is chronological.
	sort.Strings(days)
	if len(days) > overviewChartDays {
		days = days[len(days)-overviewChartDays:]
	}

	// The organisations that carry cost over the kept days, in the order of the
	// table above the chart so a colour means the same thing on both.
	drawn := make(map[model.OrgID]bool)
	for _, day := range days {
		for orgID, cost := range perDay[day] {
			if cost > 0 {
				drawn[orgID] = true
			}
		}
	}
	if len(drawn) == 0 {
		return component.OverviewCostSeries{}
	}

	orgIDs := make([]model.OrgID, 0, len(drawn))
	for orgID := range drawn {
		orgIDs = append(orgIDs, orgID)
	}
	sort.SliceStable(orgIDs, func(a, b int) bool {
		ra, oka := order[orgIDs[a]]
		rb, okb := order[orgIDs[b]]
		switch {
		case oka && okb:
			return ra < rb
		case oka:
			return true
		case okb:
			return false
		default:
			return orgIDs[a] < orgIDs[b]
		}
	})

	series := make([]component.OverviewSeries, 0, len(orgIDs))
	for i, orgID := range orgIDs {
		name, known := names[orgID]
		if !known {
			name = string(orgID)
		}

		values := make([]float64, len(days))
		for d, day := range days {
			// Costs are stored in micro-units of currency; the chart plots the
			// currency itself, as the org dashboard does.
			values[d] = float64(perDay[day][orgID]) / 1_000_000
		}

		series = append(series, component.OverviewSeries{
			OrgName:    name,
			ColorClass: component.OverviewOrgColor(i),
			Color:      common.ChartColor(i),
			Values:     values,
		})
	}

	// Les clés restent en ISO pour les recherches ci-dessus ; l'axe, lui, se lit
	// dans la même écriture que les autres graphiques de coût.
	labels := make([]string, 0, len(days))
	for _, day := range days {
		labels = append(labels, common.FormatDayLabel(day))
	}

	return component.OverviewCostSeries{Labels: labels, Series: series}
}

// convert moves an amount between currencies, falling back to the raw amount
// when no rate is available — a missing rate must not blank out a dashboard.
func (h *Handler) convert(ctx context.Context, amount int64, from, to string) int64 {
	if amount == 0 || from == "" || from == to {
		return amount
	}
	converted, err := h.exchangeRateService.Convert(ctx, amount, from, to)
	if err != nil {
		slog.WarnContext(ctx, "could not convert amount",
			slogx.Error(errors.WithStack(err)), slog.String("from", from), slog.String("to", to))
		return amount
	}
	return converted
}
