package component

import (
	"fmt"
	"sort"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/templui/component/chart"
)

// SeriesBucket is the granularity the cost chart of a period is drawn on.
type SeriesBucket string

const (
	// SeriesBucketDay charts one bar per calendar day.
	SeriesBucketDay SeriesBucket = "day"
	// SeriesBucketMonth charts one bar per calendar month.
	SeriesBucketMonth SeriesBucket = "month"
)

// RangeBucket returns the granularity a period is charted on: a day beyond a
// quarter would mean up to 365 bars, which stops being a shape and becomes a
// texture, so the long periods are charted by month.
func RangeBucket(r string) SeriesBucket {
	switch r {
	case "90d", "180d", "365d":
		return SeriesBucketMonth
	default:
		return SeriesBucketDay
	}
}

// CostSeriesTitle names the cost chart after the bucket it actually draws.
func CostSeriesTitle(r string) string {
	if RangeBucket(r) == SeriesBucketMonth {
		return "Coût par mois"
	}
	return "Coût par jour"
}

// CostSeries turns the per-day cost sub-totals of a period into a continuous
// series: one point per bucket between `since` and `until`, the empty ones
// included.
//
// The store only returns the days that carry usage. Charting those alone draws a
// period out of the days that happened to have traffic — two bars a month apart
// sit side by side, and a quiet week is indistinguishable from a week that was
// never in the window. Filling the gaps is what makes the horizontal axis a
// timeline again.
//
// `perDay` is keyed by calendar day, "YYYY-MM-DD", in micro-units of currency,
// as returned by AggregateCostByDimension on the day dimension.
func CostSeries(perDay map[string]int64, since, until time.Time, r string) []ChartDataPoint {
	return StackedCostSeries([]map[string]int64{perDay}, since, until, r)[0]
}

// StackedCostSeries is CostSeries for several series drawn on the same axis —
// the pay-as-you-go spend and the value covered by a subscription, stacked in
// one bar. Every returned series has the same buckets in the same order, so the
// i-th point of each lands on the same bar; a bucket only one series fills is
// charted at zero in the others.
func StackedCostSeries(perDay []map[string]int64, since, until time.Time, r string) [][]ChartDataPoint {
	out := make([][]ChartDataPoint, len(perDay))
	if until.Before(since) {
		return out
	}

	bucket := RangeBucket(r)
	totals := make([]map[string]int64, len(perDay))
	for i, series := range perDay {
		totals[i] = make(map[string]int64, len(series))
		for day, cost := range series {
			key, err := seriesKey(day, bucket)
			if err != nil {
				// A key the store did not produce: keep it out of the timeline rather
				// than guess where it belongs.
				continue
			}
			totals[i][key] += cost
		}
	}

	var keys []time.Time
	seen := make(map[string]bool)
	for cursor := truncate(since, bucket); !cursor.After(until); cursor = next(cursor, bucket) {
		seen[cursor.Format(keyLayout(bucket))] = true
		keys = append(keys, cursor)
	}

	// Usage recorded outside the window — the filter and the bucketing round
	// differently near the edges — would otherwise vanish from a chart whose
	// total is displayed right above it.
	var strays []string
	for _, series := range totals {
		for key := range series {
			if !seen[key] {
				seen[key] = true
				strays = append(strays, key)
			}
		}
	}
	if len(strays) > 0 {
		sort.Strings(strays)
		extra := make([]time.Time, 0, len(strays))
		for _, key := range strays {
			at, err := time.ParseInLocation(keyLayout(bucket), key, time.Local)
			if err != nil {
				continue
			}
			extra = append(extra, at)
		}
		keys = append(extra, keys...)
	}

	for i, series := range totals {
		pts := make([]ChartDataPoint, 0, len(keys))
		for _, at := range keys {
			pts = append(pts, ChartDataPoint{
				Label: seriesLabel(at, bucket),
				Value: float64(series[at.Format(keyLayout(bucket))]) / 1_000_000,
			})
		}
		out[i] = pts
	}

	return out
}

// seriesKey moves a "YYYY-MM-DD" day onto the bucket it belongs to.
func seriesKey(day string, bucket SeriesBucket) (string, error) {
	at, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		return "", fmt.Errorf("parse usage day %q: %w", day, err)
	}
	return at.Format(keyLayout(bucket)), nil
}

func keyLayout(bucket SeriesBucket) string {
	if bucket == SeriesBucketMonth {
		return "2006-01"
	}
	return "2006-01-02"
}

// truncate snaps a date to the start of its bucket, so the first bar of the
// chart is a whole day or a whole month rather than the instant the period
// happens to start at.
func truncate(at time.Time, bucket SeriesBucket) time.Time {
	at = at.In(time.Local)
	if bucket == SeriesBucketMonth {
		return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.Local)
	}
	return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.Local)
}

func next(at time.Time, bucket SeriesBucket) time.Time {
	if bucket == SeriesBucketMonth {
		return at.AddDate(0, 1, 0)
	}
	return at.AddDate(0, 0, 1)
}

// frenchMonths are the abbreviations the mockup labels its axes with ("30 juin",
// "7 juil."). Go formats month names in English only.
var frenchMonths = [...]string{
	"janv.", "févr.", "mars", "avr.", "mai", "juin",
	"juil.", "août", "sept.", "oct.", "nov.", "déc.",
}

// seriesLabel words one tick of the horizontal axis: "7 juil." for a day,
// "juil. 25" for a month — the year is carried because a yearly period shows the
// same month twice.
func seriesLabel(at time.Time, bucket SeriesBucket) string {
	month := frenchMonths[int(at.Month())-1]
	if bucket == SeriesBucketMonth {
		return fmt.Sprintf("%s %02d", month, at.Year()%100)
	}
	return fmt.Sprintf("%d %s", at.Day(), month)
}

// FormatDayLabel words an ISO day key the way the axes of the cost charts do —
// "2026-07-27" becomes "27 juil.", as the mockup labels them.
//
// It exists for the screens that bucket their days themselves, the platform
// overview in particular, so a date is written the same everywhere. A key that
// is not a date is returned unchanged: a bar keeps a label rather than none.
func FormatDayLabel(day string) string {
	at, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		return day
	}

	return seriesLabel(at, SeriesBucketDay)
}

// HasValue reports whether any point of a series is non-zero.
func HasValue(pts []ChartDataPoint) bool {
	for _, p := range pts {
		if p.Value != 0 {
			return true
		}
	}
	return false
}

// CostChartData lays the series of CostChart out for Chart.js: the
// pay-as-you-go spend first, so it sits at the base of each bar, then the value
// covered by a subscription, only when there is some.
func CostChartData(payg, covered []ChartDataPoint, currency string) chart.Data {
	if !HasValue(covered) {
		return chart.Data{
			Labels: ChartLabels(payg),
			Datasets: []chart.Dataset{{
				Label:           currency,
				Data:            ChartValues(payg),
				BackgroundColor: ChartColor(0),
			}},
		}
	}

	return chart.Data{
		Labels: ChartLabels(payg),
		Datasets: []chart.Dataset{
			{
				Label:           "À l'usage (" + currency + ")",
				Data:            ChartValues(payg),
				BackgroundColor: ChartColor(0),
			},
			{
				Label:           "Couvert par abonnement (" + currency + ")",
				Data:            ChartValues(covered),
				BackgroundColor: ChartCoveredColor,
			},
		},
	}
}

// ProviderCostValue words the figure closing a provider's row in a cost
// breakdown. A provider billed by subscription gets its equivalent PAYG value,
// marked as such: it was not charged per request.
func ProviderCostValue(value float64, currency string, covered bool) string {
	formatted := FormatCost(int64(value*1_000_000), currency)
	if covered {
		return formatted + " · forfait"
	}
	return formatted
}

// PlanCoveredLabels returns the chart labels of the providers whose cost is
// covered by a subscription, so a breakdown keyed by label can tell which of its
// figures are a value rather than an amount billed. A provider without a name is
// labelled by its id, as the breakdowns do.
func PlanCoveredLabels(covered map[model.ProviderID]bool, names map[model.ProviderID]string) map[string]bool {
	labels := make(map[string]bool, len(covered))
	for pid := range covered {
		label := names[pid]
		if label == "" {
			label = string(pid)
		}
		labels[label] = true
	}
	return labels
}
