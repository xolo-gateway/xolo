package component

import (
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

func TestFormatModelBudgets(t *testing.T) {
	value := func(v int64) *int64 { return &v }
	got := formatModelBudgets(map[string]model.PlanBudget{
		"kimi-k3":                  {ValueBudget: value(3_000_000)},
		"deepseek-v4.1-flash":      {ValueBudget: value(16_500_000), TokenBudget: value(50_000_000)},
		"longcat-2.5-preview-free": {},
	})

	// Sorted by model, in the syntax the form parses back.
	want := "deepseek-v4.1-flash = 16.5, 50000000 tokens\n" +
		"kimi-k3 = 3\n" +
		"longcat-2.5-preview-free = illimité\n"
	if got != want {
		t.Errorf("formatModelBudgets() =\n%s\nwant\n%s", got, want)
	}
}
