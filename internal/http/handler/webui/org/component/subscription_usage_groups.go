package component

import (
	"cmp"
	"slices"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// subscriptionVisibleRows is the number of gauges a constraint shows before the
// least constrained ones fold behind a toggle.
const subscriptionVisibleRows = 3

// subscriptionDangerPct is the percentage from which a gauge turns red. A gauge
// past it is never folded away.
const subscriptionDangerPct = 90

// subscriptionGaugeKind names what a gauge measures.
type subscriptionGaugeKind int

const (
	gaugeNone subscriptionGaugeKind = iota
	gaugeTokens
	gaugeValue
	gaugeConcurrency
)

// subscriptionGaugeRow is one compact line of a constraint group: one upstream
// model of a per-model constraint, or the single line of a provider-wide or
// concurrency constraint.
type subscriptionGaugeRow struct {
	Usage SubscriptionConstraintUsage
	// Kind is the budget the bar shows: the most constrained of the two when the
	// constraint has both a token and a value budget. gaugeNone means no bar.
	Kind subscriptionGaugeKind
	Pct  int
	Mode model.FairShareMode
	// OtherKind is the second budget, shown as text rather than as a second bar.
	OtherKind subscriptionGaugeKind
	OtherPct  int
	// OtherMode is the regime of the second budget. When it equals Mode, the
	// regime holds for both budgets and is said by a single badge.
	OtherMode model.FairShareMode
}

// pinned reports whether the row must stay visible whatever its rank: an
// exhausted gauge, a gauge in the red, or a share that needs accounting for.
func (r subscriptionGaugeRow) pinned() bool {
	return r.Usage.Exhausted || r.Pct >= subscriptionDangerPct ||
		notableFairShare(r.Mode) || notableFairShare(r.OtherMode)
}

// subscriptionConstraintGroup gathers the rows of one plan constraint, so what
// they share (label, duration, window kind, reset time) is said once.
type subscriptionConstraintGroup struct {
	Constraint model.PlanConstraint
	Anchored   bool
	ResetAt    time.Time
	// Kind is the gauge kind shared by every row, said in the group header;
	// gaugeNone when the rows differ (or have no bar) and each says its own.
	Kind subscriptionGaugeKind
	// IdlePerModel is set for a per-model constraint no model has consumed in
	// the window: its single row stands for every model.
	IdlePerModel  bool
	ShareDegraded bool
	// Rows are sorted by percentage, descending. Folded holds the least
	// constrained ones, hidden behind a toggle that shows FoldedPeak, the
	// highest percentage hidden, and FoldedUnlimited, the number of hidden
	// rows without a bar, which have no percentage to sum up.
	Rows            []subscriptionGaugeRow
	Folded          []subscriptionGaugeRow
	FoldedPeak      int
	FoldedUnlimited int
}

// groupSubscriptionConstraints groups the flat list of constraint usages a
// builder produces (one entry per constraint and upstream model) by
// constraint, groups keeping the order of their first entry.
func groupSubscriptionConstraints(usages []SubscriptionConstraintUsage) []subscriptionConstraintGroup {
	var groups []subscriptionConstraintGroup
	for _, cu := range usages {
		if cu.Constraint.Kind != model.ConstraintRollingWindow && cu.Constraint.Kind != model.ConstraintConcurrency {
			continue
		}
		i := slices.IndexFunc(groups, func(g subscriptionConstraintGroup) bool {
			return sameConstraint(g.Constraint, cu.Constraint)
		})
		if i < 0 {
			i = len(groups)
			groups = append(groups, subscriptionConstraintGroup{
				Constraint: cu.Constraint,
				Anchored:   cu.Anchored,
				ResetAt:    cu.ResetAt,
			})
		}
		g := &groups[i]
		if cu.Constraint.PerModel() && cu.UpstreamModel == "" {
			g.IdlePerModel = true
		}
		if cu.ShareDegraded {
			g.ShareDegraded = true
		}
		g.Rows = append(g.Rows, newSubscriptionGaugeRow(cu))
	}
	for i := range groups {
		groups[i].finish()
	}
	return groups
}

// sameConstraint reports whether two entries come from the same plan
// constraint: everything the group header says must agree. Budgets are not
// compared: the models of one per-model constraint may each have their own
// (ModelBudgets), and every row renders its own.
func sameConstraint(a, b model.PlanConstraint) bool {
	return a.Kind == b.Kind && a.Label == b.Label && a.Duration == b.Duration && a.Scope == b.Scope &&
		sameAnchor(a.WindowAnchor, b.WindowAnchor) && sameMaxConcurrent(a.MaxConcurrent, b.MaxConcurrent)
}

func sameMaxConcurrent(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameAnchor(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func newSubscriptionGaugeRow(cu SubscriptionConstraintUsage) subscriptionGaugeRow {
	row := subscriptionGaugeRow{Usage: cu}
	c := cu.Constraint
	if c.Kind == model.ConstraintConcurrency {
		if c.MaxConcurrent != nil {
			row.Kind = gaugeConcurrency
			row.Pct = clampPct(int64(cu.InFlight), int64(*c.MaxConcurrent))
		}
		return row
	}

	hasTokens := c.TokenBudget != nil && *c.TokenBudget > 0
	hasValue := c.ValueBudget != nil && *c.ValueBudget > 0
	tokens := subscriptionGaugeRow{Kind: gaugeTokens, Pct: clampPct(cu.TokensUsed, budgetOrZero(c.TokenBudget)), Mode: cu.TokenMode}
	value := subscriptionGaugeRow{Kind: gaugeValue, Pct: clampPct(cu.ValueUsed, budgetOrZero(c.ValueBudget)), Mode: cu.ValueMode}
	switch {
	case hasTokens && hasValue:
		bar, other := tokens, value
		if value.Pct > tokens.Pct {
			bar, other = value, tokens
		}
		row.Kind, row.Pct, row.Mode = bar.Kind, bar.Pct, bar.Mode
		row.OtherKind, row.OtherPct, row.OtherMode = other.Kind, other.Pct, other.Mode
	case hasTokens:
		row.Kind, row.Pct, row.Mode = tokens.Kind, tokens.Pct, tokens.Mode
	case hasValue:
		row.Kind, row.Pct, row.Mode = value.Kind, value.Pct, value.Mode
	}
	return row
}

func budgetOrZero(b *int64) int64 {
	if b == nil {
		return 0
	}
	return *b
}

// finish sorts the rows, settles the shared gauge kind and folds the least
// constrained rows.
func (g *subscriptionConstraintGroup) finish() {
	slices.SortStableFunc(g.Rows, func(a, b subscriptionGaugeRow) int {
		if (a.Kind == gaugeNone) != (b.Kind == gaugeNone) {
			// Rows without a bar go last: they have no percentage to rank on.
			if a.Kind == gaugeNone {
				return 1
			}
			return -1
		}
		if a.Kind == gaugeNone {
			// Without a percentage, the most consumed model comes first.
			return cmp.Compare(b.Usage.TokensUsed, a.Usage.TokensUsed)
		}
		return cmp.Compare(b.Pct, a.Pct)
	})

	g.Kind = g.Rows[0].Kind
	for _, r := range g.Rows[1:] {
		if r.Kind != g.Kind {
			g.Kind = gaugeNone
			break
		}
	}

	var visible, folded []subscriptionGaugeRow
	for i, r := range g.Rows {
		if i < subscriptionVisibleRows || r.pinned() {
			visible = append(visible, r)
		} else {
			folded = append(folded, r)
		}
	}
	// A toggle hiding a single line takes the room of that line.
	if len(folded) < 2 {
		return
	}
	g.Rows, g.Folded = visible, folded
	for _, r := range folded {
		g.FoldedPeak = max(g.FoldedPeak, r.Pct)
		if r.Kind == gaugeNone {
			g.FoldedUnlimited++
		}
	}
}

// subscriptionPeak is the most constrained gauge of a provider card, summed up
// in its header.
type subscriptionPeak struct {
	Pct           int
	Label         string
	UpstreamModel string
}

// peakOf returns the most constrained gauge across the groups, and false when
// there is nothing worth summing up: no gauge above zero, or a single gauge the
// card already shows in full. Folded rows are not scanned: each group's rows
// are sorted, so its most constrained one is never folded.
func peakOf(groups []subscriptionConstraintGroup) (subscriptionPeak, bool) {
	var peak subscriptionPeak
	rows := 0
	for _, g := range groups {
		rows += len(g.Rows) + len(g.Folded)
		for _, r := range g.Rows {
			if r.Kind != gaugeNone && r.Pct > peak.Pct {
				peak = subscriptionPeak{Pct: r.Pct, Label: g.Constraint.Label, UpstreamModel: r.Usage.UpstreamModel}
			}
		}
	}
	return peak, rows > 1 && peak.Pct > 0
}

// notableFairShare reports whether an allocation regime carries a badge: the
// nominal one does not.
func notableFairShare(mode model.FairShareMode) bool {
	return mode == model.FairShareModeThrottled || mode == model.FairShareModeCapped || mode == model.FairShareModeHappyHour
}
