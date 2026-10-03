package oauth2token

// Provider describes an OAuth2 authorization server whose access tokens can be
// validated through its RFC 7662 introspection endpoint.
type Provider struct {
	// ID is the provider identifier, reused as the authn.User provider so the
	// resulting Xolo user is keyed consistently with OIDC/oidctoken logins.
	ID     string
	Issuer string
	// IntrospectionURL is the RFC 7662 introspection endpoint.
	IntrospectionURL string
	// UserInfoURL, when set, is the OIDC UserInfo endpoint used to enrich the
	// identity (email, display name) when introspection does not return them
	// (e.g. Gitea's introspection omits email).
	UserInfoURL string
	// ClientID / ClientSecret authenticate Xolo (as a resource server) to the
	// introspection endpoint, via HTTP Basic auth.
	ClientID     string
	ClientSecret string
	// RequiredScope, when set, requires the token's `scope` to contain it.
	RequiredScope string
	// RequiredAudience, when set, requires the token's `aud` to contain it.
	RequiredAudience string
}
