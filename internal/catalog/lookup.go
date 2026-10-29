package catalog

// Prefill is what the catalog knows about one model, for the model form.
// Prices are per million tokens in Currency; they are zero when no provider
// of the catalog serves the model.
type Prefill struct {
	Model    *Model
	Offering *Offering
	Provider *Provider
	Currency string
}

// Lookup finds what the catalog knows about the model a provider calls
// realModel. It prefers the offering a provider at baseURL reaches, at its
// catalog provider's endpoint or its own, then the
// offering of any provider of type providerType, then any offering, and
// falls back on the model of that ID with no prices. It returns false when
// the catalog knows nothing about it.
func (c *Catalog) Lookup(realModel, baseURL, providerType string) (Prefill, bool) {
	var best Prefill
	score := -1
	for i := range c.Providers {
		p := &c.Providers[i]
		o := p.Offering(realModel)
		if o == nil {
			continue
		}
		s := 0
		if p.ServesAt(o, baseURL) {
			s = 2
		} else if providerType != "" && p.Xolo.Type == providerType {
			s = 1
		}
		if s > score {
			best, score = Prefill{Model: c.Model(o.Model), Offering: o, Provider: p, Currency: p.Currency}, s
		}
	}
	if score >= 0 {
		return best, true
	}
	if m := c.Model(realModel); m != nil {
		return Prefill{Model: m}, true
	}
	return Prefill{}, false
}
