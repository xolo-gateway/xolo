package component

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/xolo-gateway/xolo/internal/estimator/energy"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// ChartDataPoint represents a single labeled value for bar/pie charts.
type ChartDataPoint struct {
	Label string
	Value float64 // currency units (NOT microcents)
}

// ChartLabels extracts the labels from a slice of ChartDataPoint.
func ChartLabels(pts []ChartDataPoint) []string {
	labels := make([]string, len(pts))
	for i, p := range pts {
		labels[i] = p.Label
	}
	return labels
}

// ChartValues extracts the values from a slice of ChartDataPoint.
func ChartValues(pts []ChartDataPoint) []float64 {
	vals := make([]float64, len(pts))
	for i, p := range pts {
		vals[i] = p.Value
	}
	return vals
}

// ChartShare represents a value's share of a total, as a percentage, with
// an associated chart color (cycling through the design system's palette).
//
// The color travels as a Tailwind class rather than a raw CSS value: the bar is
// rendered by progress.Progress, whose BarClass is the only colour seam that does
// not require an inline style on a component-owned element.
type ChartShare struct {
	Label string
	Pct   int
	// ColorClass is a background utility built on the --chart-* tokens
	// ("bg-chart-1"), suitable for both the legend swatch and the bar.
	ColorClass string
}

// TopNChartDataPoints returns the n highest-value points (pts must be
// sorted by descending value), aggregating the remaining ones into a
// trailing "Autres" entry.
func TopNChartDataPoints(ctx context.Context, pts []ChartDataPoint, n int) []ChartDataPoint {
	if len(pts) <= n {
		return pts
	}
	top := make([]ChartDataPoint, 0, n+1)
	top = append(top, pts[:n]...)
	var rest float64
	for _, p := range pts[n:] {
		rest += p.Value
	}
	if rest > 0 {
		top = append(top, ChartDataPoint{Label: i18n.T(ctx, "format.others"), Value: rest})
	}
	return top
}

// ChartShares converts a list of data points into percentage shares of
// their total, cycling through the design system's chart color palette.
func ChartShares(pts []ChartDataPoint) []ChartShare {
	var total float64
	for _, p := range pts {
		total += p.Value
	}
	colors := []string{"bg-chart-1", "bg-chart-2", "bg-chart-3", "bg-chart-4"}
	shares := make([]ChartShare, 0, len(pts))
	for i, p := range pts {
		pct := 0
		if total > 0 {
			pct = int(p.Value / total * 100)
		}
		shares = append(shares, ChartShare{Label: p.Label, Pct: pct, ColorClass: colors[i%len(colors)]})
	}
	return shares
}

// chartPalette is the series palette of the design system, as literal colours.
//
// The CSS tokens (--chart-1…5) cannot be used here: a chart is drawn on a
// <canvas>, and Chart.js hands the string straight to the 2D context, which does
// not resolve CSS custom properties — "var(--chart-1)" is parsed as an invalid
// colour and silently painted black. These values mirror the tokens declared in
// misc/tailwind/templui.css; the two must be changed together.
var chartPalette = []string{
	"#15678f", // --chart-1, = --primary
	"#2a9d8f", // --chart-2
	"#7b5ea7", // --chart-3
	"#e09f3e", // --chart-4
	"#c9455e", // --chart-5
}

// ChartColor returns the i-th series colour, cycling through the palette.
func ChartColor(i int) string {
	return chartPalette[i%len(chartPalette)]
}

// ChartCoveredColor is the muted blue the design system reserves for the
// "couvert par forfait" part of a stacked cost bar: the same hue as the
// pay-as-you-go series, desaturated, because it is the same spend under a
// different billing arrangement — not a different category.
const ChartCoveredColor = "#b8cfdd"

// RankedRows converts data points into leaderboard entries, each bar scaled
// against the leader rather than against the total.
//
// That is what makes a ranking readable: scaled against the total, a flat
// distribution collapses every bar into a stub and the ordering stops being
// visible — which is the only thing the list is there to show.
func RankedRows(pts []ChartDataPoint) []Ranked {
	var max float64
	for _, p := range pts {
		if p.Value > max {
			max = p.Value
		}
	}

	colors := []string{"bg-chart-1", "bg-chart-2", "bg-chart-3", "bg-chart-4", "bg-chart-5"}
	rows := make([]Ranked, 0, len(pts))
	for i, p := range pts {
		pct := 0
		if max > 0 {
			pct = int(p.Value / max * 100)
		}
		rows = append(rows, Ranked{
			Label:      p.Label,
			Value:      p.Value,
			Pct:        pct,
			ColorClass: colors[i%len(colors)],
		})
	}

	return rows
}

// CurrencySymbol returns the display symbol for an ISO 4217 currency code.
func CurrencySymbol(currency string) string {
	switch currency {
	case "EUR":
		return "€"
	case "GBP":
		return "£"
	case "JPY":
		return "¥"
	case "CHF":
		return "CHF "
	case "CAD":
		return "CA$"
	case "AUD":
		return "AU$"
	default:
		return "$" // USD
	}
}

// French typography puts a space inside a number and before a unit, and both
// must be unbreakable or a figure splits across two lines in the middle of a
// table column. They are named constants rather than inline literals because
// the two characters are invisible in a diff: a plain space typed by mistake
// would pass review unnoticed.
const (
	// groupSep separates thousands: narrow no-break space, U+202F.
	groupSep = "\u202f"
	// unitSep precedes a unit or a currency symbol: no-break space, U+00A0.
	unitSep = "\u00a0"
)

// FormatDecimal renders a float in the French convention the whole UI uses:
// narrow no-break space between thousands, comma as the decimal mark.
//
// Go's fmt has no locale, so the grouping is inserted here rather than left to
// each call site — which is how the same figure ended up printed three different
// ways across the screens.
func FormatDecimal(v float64, decimals int) string {
	s := strconv.FormatFloat(v, 'f', decimals, 64)

	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}

	whole, frac, _ := strings.Cut(s, ".")

	var b strings.Builder
	for i, digit := range whole {
		// A separator goes before every digit whose distance to the end is a
		// multiple of three — except at the very start of the number.
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteString(groupSep)
		}
		b.WriteRune(digit)
	}

	out := sign + b.String()
	if frac != "" {
		out += "," + frac
	}

	return out
}

// FormatCost formats an absolute cost stored in microcents (1 microcent =
// $0.000001) as an amount of money: "1 284,40 €".
//
// The precision follows the magnitude. Two decimals is what an invoice shows,
// but a single LLM call costs fractions of a cent, and rounding those to "0,00 €"
// would make every row of the requests table read as free.
func FormatCost(v int64, currency string) string {
	amount := float64(v) / 1_000_000

	decimals := 2
	if amount != 0 && math.Abs(amount) < 1 {
		decimals = 4
	}

	return FormatDecimal(amount, decimals) + unitSep + CurrencySymbol(currency)
}

// FormatCostCompact formats a cost stored in microcents, switching to k/M/B
// suffixes once the amount reaches the thousands — the form a KPI cell needs to
// stay on one line.
func FormatCostCompact(v int64, currency string) string {
	amount := float64(v) / 1_000_000
	abs := math.Abs(amount)
	symbol := unitSep + CurrencySymbol(currency)

	switch {
	case abs >= 1_000_000_000:
		return FormatDecimal(amount/1_000_000_000, 2) + unitSep + "B" + symbol
	case abs >= 1_000_000:
		return FormatDecimal(amount/1_000_000, 2) + unitSep + "M" + symbol
	case abs >= 1_000:
		return FormatDecimal(amount/1_000, 2) + unitSep + "k" + symbol
	default:
		return FormatCost(v, currency)
	}
}

// FormatCount formats an integer count: grouped in full below a million
// ("31 940"), suffixed above it ("48,2 M"), which is where the digits stop
// carrying meaning and start costing width.
func FormatCount(n int64) string {
	f := float64(n)
	abs := math.Abs(f)

	switch {
	case abs >= 1_000_000_000:
		return FormatDecimal(f/1_000_000_000, 1) + unitSep + "B"
	case abs >= 1_000_000:
		return FormatDecimal(f/1_000_000, 1) + unitSep + "M"
	default:
		return FormatDecimal(f, 0)
	}
}

// UsagePercent returns the percentage of budget consumed by used, capped at 100.
func UsagePercent(used int64, budget *int64) int {
	if budget == nil || *budget == 0 {
		return 0
	}
	pct := int(used * 100 / *budget)
	if pct > 100 {
		return 100
	}
	return pct
}

// QuotaBarClass returns the progress bar fill utility for a budget usage
// percentage. It is a class rather than a CSS value so it can be handed to
// progress.Progress's BarClass — templui's own Variant palette (bg-green-500,
// bg-yellow-500) sits outside the design system's tokens.
func QuotaBarClass(pct int) string {
	if pct > 90 {
		return "bg-destructive"
	}
	if pct > 70 {
		return "bg-chart-3"
	}
	return "bg-chart-2"
}

// FormatEnergyWh formats an energy value with auto-scaling (kWh, Wh, mWh, µWh).
func FormatEnergyWh(wh float64) string {
	if wh <= 0 {
		return "—"
	}
	if wh >= 1000 {
		return fmt.Sprintf("%.3f kWh", wh/1000)
	}
	if wh >= 1 {
		return fmt.Sprintf("%.3f Wh", wh)
	}
	if wh >= 0.001 {
		return fmt.Sprintf("%.3f mWh", wh*1000)
	}
	return fmt.Sprintf("%.3f µWh", wh*1_000_000)
}

// FormatCO2Grams formats a CO₂ quantity in grams with auto-scaling (t, kg, g, mg, µg).
func FormatCO2Grams(g float64) string {
	if g <= 0 {
		return "—"
	}
	if g >= 1_000_000 {
		return fmt.Sprintf("%.3f tCO₂", g/1_000_000)
	}
	if g >= 1000 {
		return fmt.Sprintf("%.3f kgCO₂", g/1000)
	}
	if g >= 1 {
		return fmt.Sprintf("%.3f gCO₂", g)
	}
	if g >= 0.001 {
		return fmt.Sprintf("%.3f mgCO₂", g*1000)
	}
	return fmt.Sprintf("%.3f µgCO₂", g*1_000_000)
}

// FormatCO2ToCarKilometers converts CO2 grams to equivalent car kilometers
// based on ADEME data (~109g CO2/km for petrol vehicle).
func FormatCO2ToCarKilometers(ctx context.Context, grams float64) string {
	if grams <= 0 {
		return ""
	}
	km := grams / 109
	if km < 1 {
		return i18n.T(ctx, "format.co2_car_meters", int(km*1000))
	}
	if km < 1000 {
		return i18n.T(ctx, "format.co2_car_km", int(km))
	}
	return i18n.T(ctx, "format.co2_car_km_decimal", FormatNumber(km))
}

// FormatEnergyToHuman converts energy in Wh to human-equivalent appliance usage.
func FormatEnergyToHuman(wh float64) string {
	if wh <= 0 {
		return ""
	}
	return energy.HumanEquivalent(wh)
}

// FormatNumber formats a float with one decimal place.
func FormatNumber(n float64) string {
	return fmt.Sprintf("%.1f", n)
}
