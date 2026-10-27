package component

import (
	"context"
	"strconv"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	commonComp "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/templui/component/chart"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// OverviewPageVModel backs the platform overview, the landing screen of the
// console.
type OverviewPageVModel struct {
	AppLayoutVModel commonComp.AppLayoutVModel

	// Currency is the reference every figure of the screen is converted into,
	// since organisations may each bill in a different one.
	Currency string
	// Since is the start of the reporting window.
	Since time.Time

	TotalOrgs     int
	TotalMembers  int
	TotalCost     int64
	TotalTokens   int64
	TotalRequests int64

	Orgs []OverviewOrg
	// CostSeries is the stacked cost histogram: one bar per day, one series per
	// organisation.
	CostSeries OverviewCostSeries
}

// OverviewOrg is one organisation of the overview, with its usage over the
// window.
type OverviewOrg struct {
	ID       model.OrgID
	Name     string
	Slug     string
	Active   bool
	Members  int
	Cost     int64
	Tokens   int64
	Requests int64
	// ColorClass is the chart colour the organisation is drawn with, shared by
	// the histogram legend and its segments.
	ColorClass string
}

// OverviewCostSeries is the stacked cost histogram of the overview: the days of
// the window, and one series per organisation that consumed on it.
//
// The legend is drawn from Series, never from the organisation table: the chart
// only carries pay-as-you-go cost, so an organisation billed entirely on a
// subscription has a cost in the table and nothing to draw here. Advertising it
// in the legend would announce a colour the reader cannot find.
type OverviewCostSeries struct {
	Labels []string
	Series []OverviewSeries
}

// OverviewSeries is one organisation's contribution, aligned on Labels: a day
// without usage carries a zero, so every series has the same length.
type OverviewSeries struct {
	OrgName string
	// ColorClass inks the legend chip, Color the bars — the same palette entry
	// in the two notations Tailwind and Chart.js each need.
	ColorClass string
	Color      string
	Values     []float64
}

// overviewOrgColors is the fixed series order of the design system. Beyond five
// organisations the palette repeats: the chart is a shape, and the table right
// under it carries the exact figures.
var overviewOrgColors = []string{"bg-chart-1", "bg-chart-2", "bg-chart-3", "bg-chart-4", "bg-chart-5"}

// OverviewOrgColor returns the chart colour of the nth organisation.
func OverviewOrgColor(i int) string {
	return overviewOrgColors[i%len(overviewOrgColors)]
}

// overviewDatasets turns the series into what templui's chart expects: one bar
// dataset per organisation, all stacked on the same day.
func overviewDatasets(vmodel OverviewPageVModel) []chart.Dataset {
	datasets := make([]chart.Dataset, 0, len(vmodel.CostSeries.Series))
	for _, series := range vmodel.CostSeries.Series {
		datasets = append(datasets, chart.Dataset{
			Label:           series.OrgName,
			Data:            series.Values,
			BackgroundColor: series.Color,
		})
	}
	return datasets
}

// overviewSubtitle states the scope of the figures, as the mockup does.
func overviewSubtitle(ctx context.Context, vmodel OverviewPageVModel) string {
	return i18n.T(ctx, "admin.overview.subtitle",
		strconv.Itoa(vmodel.TotalOrgs), strconv.Itoa(vmodel.TotalMembers), vmodel.Currency,
	)
}
