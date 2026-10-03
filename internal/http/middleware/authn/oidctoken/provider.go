package oidctoken

type Provider struct {
	ClientID     string
	ID           string
	Label        string
	Icon         string
	DiscoveryURL string
	Issuer       string
	JWKSURL      string
	CookieNames  []string
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
