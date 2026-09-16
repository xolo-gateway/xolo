package component

// ProviderTypes lists the provider types an administrator can pick in the
// provider form. Every entry must be registered in the genai provider
// registry by an import in the packages resolving providers at runtime
// (internal/adapter/proxy and this handler), which a unit test enforces.
var ProviderTypes = []string{"openai", "anthropic", "mistral", "openrouter"}
