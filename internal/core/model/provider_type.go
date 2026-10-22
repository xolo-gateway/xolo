package model

// ProviderTypes lists the provider types an organization can declare. Every
// entry must be registered in the genai provider registry by an import in
// the packages that resolve providers at runtime (the proxy router and the
// provider web handler), which a test in each of them enforces.
func ProviderTypes() []string {
	return []string{"openai", "openai-responses", "anthropic", "gemini", "mistral", "openrouter", "typesafe"}
}

// IsKnownProviderType reports whether t is one of ProviderTypes.
func IsKnownProviderType(t string) bool {
	for _, known := range ProviderTypes() {
		if known == t {
			return true
		}
	}
	return false
}

// providerTypeKinds lists the kinds of model the API of a provider type
// serves, for the types that serve more than completion, or something else: a
// test of the proxy router checks it against the genai provider registry.
// Every type missing here serves completion only. The first kind of a type is
// the one its new models take, see DefaultProviderTypeKind.
var providerTypeKinds = map[string][]ModelKind{
	"openai":           {ModelKindCompletion, ModelKindEmbeddings},
	"openai-responses": {ModelKindCompletion, ModelKindEmbeddings},
	"mistral":          {ModelKindCompletion, ModelKindEmbeddings},
	"openrouter":       {ModelKindCompletion, ModelKindEmbeddings, ModelKindDecision},
	"typesafe":         {ModelKindDecision},
}

// ProviderTypeKinds returns the kinds of model the API of the provider type t
// serves.
func ProviderTypeKinds(t string) []ModelKind {
	if kinds, ok := providerTypeKinds[t]; ok {
		return kinds
	}
	return []ModelKind{ModelKindCompletion}
}

// DefaultProviderTypeKind is the kind a new model served through the API of
// the provider type t takes when none is chosen: completion, but for a type
// that serves no completion.
func DefaultProviderTypeKind(t string) ModelKind {
	return ProviderTypeKinds(t)[0]
}

// ProviderTypeServes reports whether the API of the provider type t serves
// models of kind k.
func ProviderTypeServes(t string, k ModelKind) bool {
	for _, served := range ProviderTypeKinds(t) {
		if served == KindOrDefault(k) {
			return true
		}
	}
	return false
}
