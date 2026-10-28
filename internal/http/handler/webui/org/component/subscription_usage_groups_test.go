package component

import (
	"context"
	stdhtml "html"
	"strings"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

func perModelUsage(label string, budget int64, upstream string, used int64) SubscriptionConstraintUsage {
	return SubscriptionConstraintUsage{
		Constraint: model.PlanConstraint{
			Kind:        model.ConstraintRollingWindow,
			Label:       label,
			Duration:    model.PlanDuration(5 * time.Hour),
			Scope:       model.PlanScopeModel,
			TokenBudget: &budget,
		},
		UpstreamModel: upstream,
		TokensUsed:    used,
	}
}

func rowModels(rows []subscriptionGaugeRow) []string {
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Usage.UpstreamModel)
	}
	return names
}

func TestGroupSubscriptionConstraints_GroupsByConstraintAndSortsByPercentage(t *testing.T) {
	groups := groupSubscriptionConstraints([]SubscriptionConstraintUsage{
		perModelUsage("5h", 100, "a", 1),
		perModelUsage("5h", 100, "b", 27),
		perModelUsage("5h", 100, "c", 3),
		perModelUsage("week", 100, "a", 44),
	})

	if len(groups) != 2 {
		t.Fatalf("got %d groups, want one per constraint", len(groups))
	}
	if got := strings.Join(rowModels(groups[0].Rows), ","); got != "b,c,a" {
		t.Errorf("rows = %s, want them sorted by percentage, descending", got)
	}
	if groups[0].Kind != gaugeTokens {
		t.Errorf("group kind = %v, want tokens shared by every row", groups[0].Kind)
	}
	if len(groups[0].Folded) != 0 {
		t.Errorf("folded %d rows, want none within the first %d", len(groups[0].Folded), subscriptionVisibleRows)
	}
}

func TestGroupSubscriptionConstraints_FoldsTheLeastConstrainedButNeverWhatMatters(t *testing.T) {
	exhausted := perModelUsage("5h", 100, "exhausted", 0)
	exhausted.Exhausted = true
	throttled := perModelUsage("5h", 100, "throttled", 0)
	throttled.TokenMode = model.FairShareModeThrottled

	groups := groupSubscriptionConstraints([]SubscriptionConstraintUsage{
		perModelUsage("5h", 100, "m1", 50),
		perModelUsage("5h", 100, "m2", 40),
		perModelUsage("5h", 100, "m3", 30),
		perModelUsage("5h", 100, "m4", 4),
		exhausted,
		perModelUsage("5h", 100, "m5", 2),
		throttled,
		perModelUsage("5h", 100, "m6", 1),
	})

	g := groups[0]
	if got := strings.Join(rowModels(g.Rows), ","); got != "m1,m2,m3,exhausted,throttled" {
		t.Errorf("visible rows = %s, want the top %d plus the pinned ones", got, subscriptionVisibleRows)
	}
	if got := strings.Join(rowModels(g.Folded), ","); got != "m4,m5,m6" {
		t.Errorf("folded rows = %s", got)
	}
	if g.FoldedPeak != 4 {
		t.Errorf("folded peak = %d, want the highest percentage hidden", g.FoldedPeak)
	}
}

func TestGroupSubscriptionConstraints_DoesNotFoldASingleRow(t *testing.T) {
	groups := groupSubscriptionConstraints([]SubscriptionConstraintUsage{
		perModelUsage("5h", 100, "m1", 4),
		perModelUsage("5h", 100, "m2", 3),
		perModelUsage("5h", 100, "m3", 2),
		perModelUsage("5h", 100, "m4", 1),
	})
	if len(groups[0].Folded) != 0 || len(groups[0].Rows) != 4 {
		t.Errorf("folded %d rows, want a lone extra row shown rather than behind a toggle", len(groups[0].Folded))
	}
}

func TestNewSubscriptionGaugeRow_BarsTheMostConstrainedBudget(t *testing.T) {
	cu := perModelUsage("5h", 1000, "m", 100) // 10 % of the tokens
	value := int64(1000)
	cu.Constraint.ValueBudget = &value
	cu.ValueUsed = 600 // 60 % of the value
	cu.TokenMode = model.FairShareModeCapped
	cu.ValueMode = model.FairShareModeShared

	row := newSubscriptionGaugeRow(cu)
	if row.Kind != gaugeValue || row.Pct != 60 {
		t.Errorf("bar = (%v, %d), want the 60 %% value budget", row.Kind, row.Pct)
	}
	if row.OtherKind != gaugeTokens || row.OtherPct != 10 {
		t.Errorf("text gauge = (%v, %d), want the 10 %% token budget", row.OtherKind, row.OtherPct)
	}
	if row.OtherMode != model.FairShareModeCapped {
		t.Errorf("other mode = %q, want the token badge kept", row.OtherMode)
	}
}

func TestGroupSubscriptionConstraints_MixedKindsAreSaidPerRow(t *testing.T) {
	free := perModelUsage("5h", 0, "free", 100)
	free.Constraint.TokenBudget = nil
	groups := groupSubscriptionConstraints([]SubscriptionConstraintUsage{
		perModelUsage("5h", 100, "metered", 10),
		free,
	})
	if groups[0].Kind != gaugeNone {
		t.Errorf("group kind = %v, want none when a row has no bar", groups[0].Kind)
	}
	if got := strings.Join(rowModels(groups[0].Rows), ","); got != "metered,free" {
		t.Errorf("rows = %s, want the row without a bar last", got)
	}
}

func TestSubscriptionUsageSection_SaysTheSharedInformationOnce(t *testing.T) {
	var usages []SubscriptionConstraintUsage
	for _, m := range []string{"m1", "m2", "m3", "m4", "m5"} {
		cu := perModelUsage("5h", 100, m, 1)
		cu.OldestUsage = time.Now()
		usages = append(usages, cu)
	}
	usages[2].TokensUsed = 44

	provider := model.NewProvider("org-1", "Acme", "openai", "", "", "EUR")
	var out strings.Builder
	err := SubscriptionUsageSection([]SubscriptionProviderUsage{{Provider: provider, Constraints: usages}}).Render(context.Background(), &out)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html := stdhtml.UnescapeString(out.String())

	if n := strings.Count(html, "Fenêtre glissante de 5h"); n != 1 {
		t.Errorf("window described %d times, want once for the constraint", n)
	}
	if n := strings.Count(html, "se libère dans"); n != len(usages) {
		t.Errorf("countdown shown %d times, want one per model", n)
	}
	if !strings.Contains(html, "+ 2 autres modèles, jusqu'à 1%") {
		t.Error("the two least constrained models should fold behind a toggle showing their peak")
	}
	if !strings.Contains(html, "Pic : 44% · 5h · m3") {
		t.Error("the card header should sum up the most constrained gauge")
	}
	if first, second := strings.Index(html, ">m3<"), strings.Index(html, ">m1<"); first < 0 || second < 0 || first > second {
		t.Error("the most constrained model should come first")
	}
}
