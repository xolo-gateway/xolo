package oidctoken

type Provider struct {
	ID           string
	Label        string
	Icon         string
	DiscoveryURL string
	Issuer       string
	JWKSURL      string
	// ProvesIssuer tells that a token validated against Issuer can match an
	// identity declared by provisioning.
	ProvesIssuer bool
	CookieNames  []string
	// AllowedAudiences, when not empty, requires the token `aud` to contain at
	// least one of these values. Empty accepts any audience.
	AllowedAudiences []string
}

type JWKS struct {
	Keys []JWK `json:"keys"`
}

type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	K   string `json:"k"`
}
