package component

import (
	"context"
	"testing"

	"golang.org/x/text/language"

	"github.com/xolo-gateway/xolo/internal/i18n"
)

// The expected values below are written with explicit escapes: the two
// separators are invisible, so a literal " " in a test would happily accept a
// regression back to an ASCII space.
const (
	nnbsp = " " // narrow no-break space — French thousands
	nbsp  = " " // no-break space — before a unit
)

func inLanguage(t *testing.T, tag language.Tag) context.Context {
	t.Helper()
	bundle, err := i18n.Load()
	if err != nil {
		t.Fatal(err)
	}
	return bundle.WithLanguage(context.Background(), tag)
}

func TestFormatCost(t *testing.T) {
	fr, en, es := inLanguage(t, language.French), inLanguage(t, language.English), inLanguage(t, language.Spanish)
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		microc   int64
		currency string
		want     string
	}{
		{"amount above one unit keeps two decimals", fr, 1_284_400_000, "EUR", "1" + nnbsp + "284,40" + nbsp + "€"},
		{"zero is not a fraction", fr, 0, "EUR", "0,00" + nbsp + "€"},
		{"sub-cent call cost keeps four decimals", fr, 41_200, "EUR", "0,0412" + nbsp + "€"},
		{"thousands are grouped", fr, 9_504_560_000, "EUR", "9" + nnbsp + "504,56" + nbsp + "€"},
		{"negative amounts keep their sign", fr, -1_500_000, "USD", "-1,50" + nbsp + "$"},
		{"a context without language is French", context.Background(), 1_284_400_000, "EUR", "1" + nnbsp + "284,40" + nbsp + "€"},
		{"English puts the symbol first", en, 1_284_400_000, "EUR", "€1,284.40"},
		{"English keeps four decimals below one unit", en, 41_200, "USD", "$0.0412"},
		{"English puts the sign before the symbol", en, -1_500_000, "CAD", "-CA$1.50"},
		{"Spanish leaves four digits ungrouped", es, 1_284_400_000, "EUR", "1284,40" + nbsp + "€"},
		{"Spanish groups five digits", es, 12_840_400_000, "EUR", "12.840,40" + nbsp + "€"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatCost(tc.ctx, tc.microc, tc.currency); got != tc.want {
				t.Errorf("FormatCost(%d, %q) = %q, want %q", tc.microc, tc.currency, got, tc.want)
			}
		})
	}
}

func TestFormatCostCompact(t *testing.T) {
	fr, en := inLanguage(t, language.French), inLanguage(t, language.English)
	for _, tc := range []struct {
		ctx  context.Context
		v    int64
		want string
	}{
		{fr, 12_346_000_000, "12,35" + nbsp + "k" + nbsp + "€"},
		{en, 12_346_000_000, "€12.35k"},
		{en, 2_500_000_000_000, "€2.50M"},
		{en, 12_000_000, "€12.00"},
	} {
		if got := FormatCostCompact(tc.ctx, tc.v, "EUR"); got != tc.want {
			t.Errorf("FormatCostCompact(%s, %d) = %q, want %q", i18n.Language(tc.ctx), tc.v, got, tc.want)
		}
	}
}

func TestFormatCount(t *testing.T) {
	fr, en, es := inLanguage(t, language.French), inLanguage(t, language.English), inLanguage(t, language.Spanish)
	for _, tc := range []struct {
		name string
		ctx  context.Context
		n    int64
		want string
	}{
		{"units are printed as-is", fr, 0, "0"},
		{"hundreds are not grouped", fr, 940, "940"},
		{"thousands are grouped in full", fr, 31_940, "31" + nnbsp + "940"},
		{"millions switch to a suffix", fr, 48_200_000, "48,2" + nbsp + "M"},
		{"billions switch to a suffix", fr, 2_400_000_000, "2,4" + nbsp + "B"},
		{"English groups with commas", en, 31_940, "31,940"},
		{"English glues the suffix", en, 48_200_000, "48.2M"},
		{"Spanish groups with dots", es, 31_940, "31.940"},
		{"Spanish leaves four digits ungrouped", es, 1_940, "1940"},
		{"Spanish suffixes with a comma", es, 48_200_000, "48,2" + nbsp + "M"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatCount(tc.ctx, tc.n); got != tc.want {
				t.Errorf("FormatCount(%d) = %q, want %q", tc.n, got, tc.want)
			}
		})
	}
}

func TestFormatDecimalGroupsFromTheRight(t *testing.T) {
	// Grouping is inserted by walking the digits left to right, so the boundary
	// cases are the ones where the first group is shorter than three digits.
	fr := inLanguage(t, language.French)
	for _, tc := range []struct {
		v    float64
		want string
	}{
		{1, "1"},
		{100, "100"},
		{1000, "1" + nnbsp + "000"},
		{10000, "10" + nnbsp + "000"},
		{100000, "100" + nnbsp + "000"},
		{1000000, "1" + nnbsp + "000" + nnbsp + "000"},
		{-1000000, "-1" + nnbsp + "000" + nnbsp + "000"},
	} {
		if got := FormatDecimal(fr, tc.v, 0); got != tc.want {
			t.Errorf("FormatDecimal(%v, 0) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

func TestFormatEnergyAndCO2FollowTheLanguage(t *testing.T) {
	fr, en := inLanguage(t, language.French), inLanguage(t, language.English)
	if got, want := FormatEnergyWh(fr, 1234.6), "1,235"+nbsp+"kWh"; got != want {
		t.Errorf("FormatEnergyWh(fr) = %q, want %q", got, want)
	}
	if got, want := FormatEnergyWh(en, 1234.6), "1.235"+nbsp+"kWh"; got != want {
		t.Errorf("FormatEnergyWh(en) = %q, want %q", got, want)
	}
	if got, want := FormatCO2Grams(en, 0.5), "500.000"+nbsp+"mgCO₂"; got != want {
		t.Errorf("FormatCO2Grams(en) = %q, want %q", got, want)
	}
}
