package model

// ProviderTypes lists the provider types an organization can declare. Every
// entry must be registered in the genai provider registry by an import in
// the packages that resolve providers at runtime (the proxy router and the
// provider web handler), which a test in each of them enforces.
func ProviderTypes() []string {
	return []string{"openai", "anthropic", "mistral", "openrouter"}
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
