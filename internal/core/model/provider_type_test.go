package model

import "testing"

// A new model is a completion model on every type that serves completions:
// reordering the kinds of a type must not turn its new models into another
// kind.
func TestDefaultProviderTypeKind(t *testing.T) {
	for _, providerType := range ProviderTypes() {
		want := ModelKindCompletion
		if !ProviderTypeServes(providerType, ModelKindCompletion) {
			want = ProviderTypeKinds(providerType)[0]
		}
		if got := DefaultProviderTypeKind(providerType); got != want {
			t.Errorf("DefaultProviderTypeKind(%q) = %q, want %q", providerType, got, want)
		}
	}
	if got := DefaultProviderTypeKind("typesafe"); got != ModelKindDecision {
		t.Errorf("DefaultProviderTypeKind(typesafe) = %q, want decision", got)
	}
}
