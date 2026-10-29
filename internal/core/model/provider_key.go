package model

import (
	"hash/fnv"
	"slices"
	"time"

	"github.com/rs/xid"
)

// ProviderKeyID identifies one API key of a provider. The provider's own key,
// the one every provider has, is PrimaryProviderKey.
type ProviderKeyID string

// PrimaryProviderKey designates the provider's own API key. Usage recorded
// before a provider had several keys carries it too.
const PrimaryProviderKey ProviderKeyID = ""

func NewProviderKeyID() ProviderKeyID {
	return ProviderKeyID(xid.New().String())
}

// ProviderKey is an API key a provider holds besides its own, such as the key
// of one member of a team subscription. A subscription provider with several
// keys meters its plan on each key and spreads its users over them; the keys
// are an administration matter, never shown to the users.
type ProviderKey struct {
	ID    ProviderKeyID `json:"id"`
	Label string        `json:"label"`
	// APIKey is encrypted at rest, decrypted on use, like the provider's own.
	APIKey string `json:"api_key"`
	// Overrides adjust, per constraint label, the provider's plan as it
	// applies to this key: a member whose subscription holds other budgets,
	// or renews on another day. A constraint without override applies as is.
	Overrides map[string]PlanConstraintOverride `json:"overrides,omitempty"`
}

// PlanConstraintOverride adjusts one constraint of a provider's plan for one of
// its keys. Each field left unset keeps the plan's.
type PlanConstraintOverride struct {
	TokenBudget *int64 `json:"token_budget,omitempty"`
	ValueBudget *int64 `json:"value_budget,omitempty"` // microcents in provider currency
	// ModelBudgets replace, model by model, the constraint's own model budgets.
	ModelBudgets map[string]PlanBudget `json:"model_budgets,omitempty"`
	// WindowAnchor moves the key's window, for a member whose subscription
	// renews on another day.
	WindowAnchor *time.Time `json:"window_anchor,omitempty"`
}

// Empty reports whether the override changes nothing.
func (o PlanConstraintOverride) Empty() bool {
	return o.TokenBudget == nil && o.ValueBudget == nil && len(o.ModelBudgets) == 0 && o.WindowAnchor == nil
}

// Apply returns the constraint as the override adjusts it.
func (o PlanConstraintOverride) Apply(c PlanConstraint) PlanConstraint {
	if o.TokenBudget != nil {
		c.TokenBudget = o.TokenBudget
	}
	if o.ValueBudget != nil {
		c.ValueBudget = o.ValueBudget
	}
	if len(o.ModelBudgets) > 0 {
		budgets := make(map[string]PlanBudget, len(c.ModelBudgets)+len(o.ModelBudgets))
		for m, b := range c.ModelBudgets {
			budgets[m] = b
		}
		for m, b := range o.ModelBudgets {
			budgets[m] = b
		}
		c.ModelBudgets = budgets
	}
	if o.WindowAnchor != nil {
		c.WindowAnchor = o.WindowAnchor
	}
	return c
}

// ProviderKeys returns every API key of a provider, its own first.
func ProviderKeys(p Provider) []ProviderKey {
	extra := p.ExtraAPIKeys()
	keys := make([]ProviderKey, 0, 1+len(extra))
	keys = append(keys, ProviderKey{ID: PrimaryProviderKey, APIKey: p.APIKey()})
	return append(keys, extra...)
}

// ProviderKeyByID returns the key of a provider with the given id.
func ProviderKeyByID(p Provider, id ProviderKeyID) (ProviderKey, bool) {
	if id == PrimaryProviderKey {
		return ProviderKey{ID: PrimaryProviderKey, APIKey: p.APIKey()}, true
	}
	for _, k := range p.ExtraAPIKeys() {
		if k.ID == id {
			return k, true
		}
	}
	return ProviderKey{}, false
}

// ProviderKeyIDs returns the ids of every API key of a provider, its own
// first.
func ProviderKeyIDs(p Provider) []ProviderKeyID {
	ids := []ProviderKeyID{PrimaryProviderKey}
	for _, k := range p.ExtraAPIKeys() {
		ids = append(ids, k.ID)
	}
	return ids
}

// KeyPoolSize is the number of API keys a provider spreads its requests over.
func KeyPoolSize(p Provider) int {
	return 1 + len(p.ExtraAPIKeys())
}

// KeyPlan is the plan of a provider as it applies to one of its keys: the
// provider's own, adjusted by the key's overrides.
func KeyPlan(p Provider, key ProviderKeyID) *SubscriptionPlan {
	plan := p.SubscriptionPlan()
	k, ok := ProviderKeyByID(p, key)
	if plan == nil || !ok || len(k.Overrides) == 0 {
		return plan
	}
	adjusted := &SubscriptionPlan{Label: plan.Label, Constraints: make([]PlanConstraint, len(plan.Constraints))}
	for i, c := range plan.Constraints {
		if o, ok := k.Overrides[c.Label]; ok {
			c = o.Apply(c)
		}
		adjusted.Constraints[i] = c
	}
	return adjusted
}

// PooledPlan is the plan of a provider as its users share it: each constraint
// holds the budgets of every key together, each key holding a plan of its own
// (see KeyPlan). Its windows are the provider's: a key whose window is moved
// is counted in its own window through KeyWindows. A provider with a single key
// keeps its plan.
func PooledPlan(p Provider) *SubscriptionPlan {
	plan := p.SubscriptionPlan()
	keys := ProviderKeyIDs(p)
	if plan == nil || len(keys) <= 1 {
		return plan
	}
	plans := make([]*SubscriptionPlan, len(keys))
	for i, key := range keys {
		plans[i] = KeyPlan(p, key)
	}
	pooled := &SubscriptionPlan{Label: plan.Label, Constraints: make([]PlanConstraint, len(plan.Constraints))}
	for i, c := range plan.Constraints {
		perKey := make([]PlanConstraint, len(plans))
		for k, kp := range plans {
			perKey[k] = kp.Constraints[i]
		}
		pooled.Constraints[i] = poolConstraint(c, perKey)
	}
	return pooled
}

// poolConstraint sums the budgets of one constraint over the keys of a pool.
// A model budget is summed the same way, a key that does not list the model
// counting at its constraint's own budget. That is the budget such a model
// holds on that key, whether the window is per model (it takes the window's
// budget) or shared (it fills the window at the window's own rate, the weight
// of a model budget equal to it): the pooled figure is what the keys hold
// together, so a model listed on one key only counts the other keys' whole
// windows. A key leaving a model unlimited adds nothing to its budget: the
// model then fills the pool at the rate of the keys that do limit it.
func poolConstraint(c PlanConstraint, perKey []PlanConstraint) PlanConstraint {
	var tokens, values []*int64
	for _, k := range perKey {
		tokens = append(tokens, k.TokenBudget)
		values = append(values, k.ValueBudget)
	}
	c.TokenBudget, c.ValueBudget = sumBudgets(tokens), sumBudgets(values)
	if c.MaxConcurrent != nil {
		v := *c.MaxConcurrent * len(perKey)
		c.MaxConcurrent = &v
	}
	models := map[string]bool{}
	for _, k := range perKey {
		for m := range k.ModelBudgets {
			models[m] = true
		}
	}
	if len(models) == 0 {
		return c
	}
	c.ModelBudgets = make(map[string]PlanBudget, len(models))
	for m := range models {
		var mt, mv []*int64
		for _, k := range perKey {
			b, listed := k.ModelBudgets[m]
			if !listed {
				b = PlanBudget{TokenBudget: k.TokenBudget, ValueBudget: k.ValueBudget}
			}
			mt, mv = append(mt, b.TokenBudget), append(mv, b.ValueBudget)
		}
		c.ModelBudgets[m] = PlanBudget{TokenBudget: sumBudgets(mt), ValueBudget: sumBudgets(mv)}
	}
	return c
}

// sumBudgets adds the budgets that are set, nil when none is.
func sumBudgets(budgets []*int64) *int64 {
	var total *int64
	for _, b := range budgets {
		if b == nil {
			continue
		}
		if total == nil {
			total = new(int64)
		}
		*total += *b
	}
	return total
}

// KeyWindows returns, for the constraint of a provider's plan with the given
// label, the start of the current window of every key whose window is moved
// by an override, or nil when none is. Usage of a pool is counted per key in
// its own window (see PlanUsageScope.KeyWindows).
func KeyWindows(p Provider, label string, now time.Time) map[ProviderKeyID]time.Time {
	plan := p.SubscriptionPlan()
	if plan == nil {
		return nil
	}
	i := slices.IndexFunc(plan.Constraints, func(c PlanConstraint) bool { return c.Label == label })
	// Only a rolling window has a start; any other would read as "since ever".
	if i < 0 || plan.Constraints[i].Kind != ConstraintRollingWindow || plan.Constraints[i].Duration.Duration() <= 0 {
		return nil
	}
	var windows map[ProviderKeyID]time.Time
	// The provider's own key holds the plan itself, never an override (see
	// ProviderKeyByID): its window is the plan's.
	for _, k := range p.ExtraAPIKeys() {
		o, ok := k.Overrides[label]
		if !ok || o.WindowAnchor == nil {
			continue
		}
		if windows == nil {
			windows = map[ProviderKeyID]time.Time{}
		}
		windows[k.ID] = o.Apply(plan.Constraints[i]).CurrentWindowStart(now)
	}
	return windows
}

// KeyAffinity orders the keys a user is not yet bound to: a stable, per-user
// pseudo-random rank, so that users arriving on an idle plan spread over the
// keys instead of all landing on the first one.
func KeyAffinity(userID UserID, key ProviderKeyID) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(userID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(key))
	return h.Sum64()
}
