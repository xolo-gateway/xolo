package model

import (
	"hash/fnv"

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

// PooledPlan is the plan of a provider as its users share it: each constraint
// counts once per key, the keys of a team subscription each holding a whole
// plan of their own. A provider with a single key keeps its plan.
func PooledPlan(p Provider) *SubscriptionPlan {
	return p.SubscriptionPlan().Pooled(KeyPoolSize(p))
}

// Pooled returns the plan with every constraint multiplied n times, or the
// plan itself when n is 1 or less.
func (p *SubscriptionPlan) Pooled(n int) *SubscriptionPlan {
	if p == nil || n <= 1 {
		return p
	}
	pooled := &SubscriptionPlan{Label: p.Label, Constraints: make([]PlanConstraint, len(p.Constraints))}
	for i, c := range p.Constraints {
		pooled.Constraints[i] = c.Pooled(n)
	}
	return pooled
}

// Pooled returns the constraint with its budgets and concurrency multiplied n
// times. The model budgets are multiplied alike, so a shared constraint weighs
// each model as it did.
func (c PlanConstraint) Pooled(n int) PlanConstraint {
	if n <= 1 {
		return c
	}
	c.TokenBudget = scaledBudget(c.TokenBudget, n)
	c.ValueBudget = scaledBudget(c.ValueBudget, n)
	if c.MaxConcurrent != nil {
		v := *c.MaxConcurrent * n
		c.MaxConcurrent = &v
	}
	if c.ModelBudgets != nil {
		budgets := make(map[string]PlanBudget, len(c.ModelBudgets))
		for m, b := range c.ModelBudgets {
			budgets[m] = PlanBudget{TokenBudget: scaledBudget(b.TokenBudget, n), ValueBudget: scaledBudget(b.ValueBudget, n)}
		}
		c.ModelBudgets = budgets
	}
	return c
}

func scaledBudget(b *int64, n int) *int64 {
	if b == nil {
		return nil
	}
	v := *b * int64(n)
	return &v
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
