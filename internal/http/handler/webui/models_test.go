package webui

import (
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/profile/component"
)

// stubLLMModel lets tests build an LLMModel with only the cost/virtual bits
// the comparator looks at, without standing up the full model.
type stubLLMModel struct {
	id         model.LLMModelID
	prompt     int64
	completion int64
	virtual    bool
}

func (s *stubLLMModel) ID() model.LLMModelID                      { return s.id }
func (s *stubLLMModel) PromptCostPer1KTokens() int64              { return s.prompt }
func (s *stubLLMModel) CachedPromptCostPer1KTokens() int64        { return s.prompt }
func (s *stubLLMModel) CompletionCostPer1KTokens() int64          { return s.completion }
func (s *stubLLMModel) IsVirtual() bool                           { return s.virtual }
func (s *stubLLMModel) ProviderID() model.ProviderID              { return "" }
func (s *stubLLMModel) OrgID() model.OrgID                        { return "" }
func (s *stubLLMModel) ProxyName() string                         { return "" }
func (s *stubLLMModel) RealModel() string                         { return "" }
func (s *stubLLMModel) Description() string                       { return "" }
func (s *stubLLMModel) Enabled() bool                             { return true }
func (s *stubLLMModel) ContextWindow() int64                      { return 0 }
func (s *stubLLMModel) OutputWindow() int64                       { return 0 }
func (s *stubLLMModel) ActiveParams() int64                       { return 0 }
func (s *stubLLMModel) TokensPerSecLow() float64                  { return 0 }
func (s *stubLLMModel) TokensPerSecHigh() float64                 { return 0 }
func (s *stubLLMModel) Capabilities() model.ModelCapabilities     { return model.ModelCapabilities{} }
func (s *stubLLMModel) CreatedAt() time.Time                      { return time.Time{} }
func (s *stubLLMModel) UpdatedAt() time.Time                      { return time.Time{} }
func (s *stubLLMModel) TokenLimitConfig() *model.TokenLimitConfig { return nil }
func (s *stubLLMModel) ExtraBody() map[string]any                 { return nil }

var _ model.LLMModel = (*stubLLMModel)(nil)

func usage(requests int64) *port.UsageAggregate {
	return &port.UsageAggregate{TotalRequests: requests}
}

func realMu(id string, prompt, completion int64, agg *port.UsageAggregate) component.ModelUsage {
	return component.ModelUsage{
		Model:     &stubLLMModel{id: model.LLMModelID(id), prompt: prompt, completion: completion},
		Aggregate: agg,
	}
}

func virtualMu(id string) component.ModelUsage {
	return component.ModelUsage{
		Model: &stubLLMModel{id: model.LLMModelID(id), virtual: true},
	}
}

// TestCompareModelUsages pins the behaviour of compareModelUsages for every
// (sortParam, order) pair: the default direction of each criterion, the
// effect of the order override, virtual-model handling, and the edge cases
// (nil aggregates, tied values).
func TestCompareModelUsages(t *testing.T) {
	t.Run("price sort pushes virtual models to the end regardless of order", func(t *testing.T) {
		v := virtualMu("v")
		cheap := realMu("cheap", 1, 1, usage(0))

		// asc: real model must outrank virtual.
		if compareModelUsages(v, cheap, "price", orderAsc) {
			t.Errorf("virtual model must NOT outrank a real model under price asc")
		}
		if !compareModelUsages(cheap, v, "price", orderAsc) {
			t.Errorf("real model with positive cost MUST outrank a virtual model under price asc")
		}
		// desc: same — virtual models have no rate to rank on.
		if compareModelUsages(v, cheap, "price", orderDesc) {
			t.Errorf("virtual model must NOT outrank a real model under price desc")
		}
		if !compareModelUsages(cheap, v, "price", orderDesc) {
			t.Errorf("real model with positive cost MUST outrank a virtual model under price desc")
		}
	})

	t.Run("price asc is ascending on prompt + completion", func(t *testing.T) {
		cheap := realMu("cheap", 1, 1, usage(0))
		expensive := realMu("expensive", 1000, 1000, usage(0))

		if !compareModelUsages(cheap, expensive, "price", orderAsc) {
			t.Errorf("cheaper model should outrank more expensive under price asc")
		}
		if compareModelUsages(expensive, cheap, "price", orderAsc) {
			t.Errorf("more expensive model must NOT outrank cheaper under price asc")
		}
	})

	t.Run("price desc reverses the per-1K ranking", func(t *testing.T) {
		cheap := realMu("cheap", 1, 1, usage(0))
		expensive := realMu("expensive", 1000, 1000, usage(0))

		if !compareModelUsages(expensive, cheap, "price", orderDesc) {
			t.Errorf("more expensive should outrank cheaper under price desc")
		}
		if compareModelUsages(cheap, expensive, "price", orderDesc) {
			t.Errorf("cheaper must NOT outrank more expensive under price desc")
		}
	})

	t.Run("two virtual models fall back to usage comparison under price sort", func(t *testing.T) {
		hi := virtualMu("v-high")
		hi.Aggregate = usage(10)
		lo := virtualMu("v-low")
		lo.Aggregate = usage(1)

		// Tie-break on usage follows the requested direction: asc puts
		// fewer requests first, desc puts more requests first.
		if compareModelUsages(hi, lo, "price", orderAsc) {
			t.Errorf("two virtual models under price asc should fall back to usage asc (fewer requests first)")
		}
		if !compareModelUsages(lo, hi, "price", orderAsc) {
			t.Errorf("two virtual models under price asc should fall back to usage asc (other direction)")
		}
		// desc: tie-break matches the desc direction.
		if !compareModelUsages(hi, lo, "price", orderDesc) {
			t.Errorf("two virtual models under price desc should fall back to usage desc")
		}
		if compareModelUsages(lo, hi, "price", orderDesc) {
			t.Errorf("two virtual models under price desc should fall back to usage desc (other direction)")
		}
	})

	t.Run("two virtual models with no usage are considered equal", func(t *testing.T) {
		a := virtualMu("v1")
		b := virtualMu("v2")

		for _, ord := range []sortOrder{orderAsc, orderDesc} {
			if compareModelUsages(a, b, "price", ord) {
				t.Errorf("two virtual models with no usage should be considered equal under price %s", ord)
			}
			if compareModelUsages(b, a, "price", ord) {
				t.Errorf("two virtual models with no usage should be considered equal under price %s (other direction)", ord)
			}
		}
	})

	t.Run("tied cost falls back to usage comparison", func(t *testing.T) {
		high := realMu("tied-high", 5, 5, usage(10))
		low := realMu("tied-low", 5, 5, usage(1))

		// Tie-break on usage follows the requested direction.
		if compareModelUsages(high, low, "price", orderAsc) {
			t.Errorf("tied cost under price asc should fall back to usage asc")
		}
		if !compareModelUsages(low, high, "price", orderAsc) {
			t.Errorf("tied cost under price asc should fall back to usage asc (other direction)")
		}
		// desc: tie-break matches the desc direction.
		if !compareModelUsages(high, low, "price", orderDesc) {
			t.Errorf("tied cost under price desc should fall back to usage desc")
		}
		if compareModelUsages(low, high, "price", orderDesc) {
			t.Errorf("tied cost under price desc should fall back to usage desc (other direction)")
		}
	})

	t.Run("tied cost with nil aggregates is equal", func(t *testing.T) {
		a := realMu("tied-nil-1", 7, 7, nil)
		b := realMu("tied-nil-2", 7, 7, nil)

		for _, ord := range []sortOrder{orderAsc, orderDesc} {
			if compareModelUsages(a, b, "price", ord) {
				t.Errorf("tied cost with nil aggregates should be considered equal under price %s", ord)
			}
			if compareModelUsages(b, a, "price", ord) {
				t.Errorf("tied cost with nil aggregates should be considered equal under price %s (other direction)", ord)
			}
		}
	})

	t.Run("nil aggregate sorts after non-nil aggregate under price tie", func(t *testing.T) {
		withAgg := realMu("with-agg", 7, 7, usage(0))
		noAgg := realMu("no-agg", 7, 7, nil)

		if !compareModelUsages(withAgg, noAgg, "price", orderAsc) {
			t.Errorf("non-nil aggregate should outrank nil aggregate under price asc")
		}
		if compareModelUsages(noAgg, withAgg, "price", orderAsc) {
			t.Errorf("nil aggregate must NOT outrank non-nil aggregate under price asc")
		}
		if !compareModelUsages(withAgg, noAgg, "price", orderDesc) {
			t.Errorf("non-nil aggregate should outrank nil aggregate under price desc")
		}
	})

	t.Run("default usage sort is descending", func(t *testing.T) {
		hi := realMu("hi", 0, 0, usage(5))
		lo := realMu("lo", 0, 0, usage(1))

		// Descending is the natural direction for usage: callers pass
		// orderDesc explicitly to match the historical behaviour.
		if !compareModelUsages(hi, lo, "", orderDesc) {
			t.Errorf("default usage sort with orderDesc should put higher usage first")
		}
		if compareModelUsages(lo, hi, "", orderDesc) {
			t.Errorf("default usage sort with orderDesc must NOT order ascending")
		}
	})

	t.Run("usage asc pushes models with no usage to the end", func(t *testing.T) {
		used := realMu("used", 0, 0, usage(5))
		unused := realMu("unused", 0, 0, nil)

		// Used model first, unused pushed to the end.
		if !compareModelUsages(used, unused, "", orderAsc) {
			t.Errorf("usage asc should put a model with usage before a model without usage")
		}
		if compareModelUsages(unused, used, "", orderAsc) {
			t.Errorf("usage asc must NOT put an unused model first")
		}
	})

	t.Run("usage asc with TotalRequests=0 is treated as unused", func(t *testing.T) {
		used := realMu("used", 0, 0, usage(5))
		zeroReq := realMu("zero-req", 0, 0, usage(0))

		if !compareModelUsages(used, zeroReq, "", orderAsc) {
			t.Errorf("usage asc should treat TotalRequests=0 as unused and push it after a model with usage")
		}
		if compareModelUsages(zeroReq, used, "", orderAsc) {
			t.Errorf("usage asc must NOT put a zero-request model first")
		}
	})

	t.Run("usage asc among used models orders ascending", func(t *testing.T) {
		high := realMu("high", 0, 0, usage(10))
		low := realMu("low", 0, 0, usage(1))

		if !compareModelUsages(low, high, "", orderAsc) {
			t.Errorf("usage asc should put lower usage first when both models have usage")
		}
		if compareModelUsages(high, low, "", orderAsc) {
			t.Errorf("usage asc must NOT put higher usage first")
		}
	})

	t.Run("usage desc falls back to natural usage descending", func(t *testing.T) {
		hi := realMu("hi", 0, 0, usage(5))
		lo := realMu("lo", 0, 0, usage(1))

		if !compareModelUsages(hi, lo, "", orderDesc) {
			t.Errorf("usage desc should put higher usage first")
		}
		if compareModelUsages(lo, hi, "", orderDesc) {
			t.Errorf("usage desc must NOT put lower usage first")
		}
	})

	t.Run("unknown sort value falls back to default usage ordering", func(t *testing.T) {
		hi := realMu("hi", 0, 0, usage(5))
		lo := realMu("lo", 0, 0, usage(1))

		if !compareModelUsages(hi, lo, "price_invalid", orderDesc) {
			t.Errorf("unknown sort values should fall back to usage desc ordering")
		}
	})

	t.Run("default sort is independent of cost", func(t *testing.T) {
		// A cheap model with no usage must NOT outrank an expensive model
		// with usage under the default sort, even when toggled asc.
		cheapUnused := realMu("cheap-unused", 1, 1, usage(0))
		expensiveUsed := realMu("expensive-used", 1000, 1000, usage(5))

		if compareModelUsages(cheapUnused, expensiveUsed, "", orderAsc) {
			t.Errorf("default sort with orderAsc should still push unused models last")
		}
		if !compareModelUsages(expensiveUsed, cheapUnused, "", orderAsc) {
			t.Errorf("default sort with orderAsc should keep used model first")
		}
		if compareModelUsages(cheapUnused, expensiveUsed, "", orderDesc) {
			t.Errorf("default sort with orderDesc should NOT be affected by cost")
		}
		if !compareModelUsages(expensiveUsed, cheapUnused, "", orderDesc) {
			t.Errorf("default sort with orderDesc should keep higher usage first regardless of cost")
		}
	})
}

func TestSortOrderString(t *testing.T) {
	if got := orderAsc.String(); got != "asc" {
		t.Errorf("orderAsc.String() = %q, want %q", got, "asc")
	}
	if got := orderDesc.String(); got != "desc" {
		t.Errorf("orderDesc.String() = %q, want %q", got, "desc")
	}
	// Unknown values map to "asc" so the URL never carries a typo through
	// the toggle.
	if got := sortOrder(99).String(); got != "asc" {
		t.Errorf("sortOrder(99).String() = %q, want %q (defensive fallback)", got, "asc")
	}
}
