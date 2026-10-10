package model

// CatalogOverrides lets an operator declare what a virtual model advertises in
// the model catalogue (GET /api/v1/models) when the card cannot be derived from
// its pipeline, or when the derived value is not the one to show. A set field
// wins over the derived one; a zero field leaves derivation in charge.
type CatalogOverrides struct {
	// ContextWindow is the context length advertised, in tokens.
	ContextWindow int64 `json:"contextWindow,omitempty"`
	// MaxOutputTokens is the completion limit advertised, in tokens.
	MaxOutputTokens int64 `json:"maxOutputTokens,omitempty"`
	// Capabilities replaces the derived capabilities as a whole when set.
	Capabilities *ModelCapabilities `json:"capabilities,omitempty"`
}

// IsZero reports whether the overrides declare nothing.
func (o *CatalogOverrides) IsZero() bool {
	return o == nil || (o.ContextWindow <= 0 && o.MaxOutputTokens <= 0 && o.Capabilities == nil)
}
