package setup

import (
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/oauth2token"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn/oidctoken"
)

// apiAuthenticatorChain orders the authenticators of the API:
//
//	oidctoken (local JWT ID token) → apiKey (database) →
//	oauth2token (remote introspection) → session.
//
// oidctoken refuses any bearer token that is no JWT of its providers without
// calling them, and verifies the others with their cached keys: an ID token
// needs neither the database nor, while its keys are known, the provider. A
// valid API key is found in the database before oauth2token, so it never
// reaches the provider either. Both keep working while the provider is down.
// oidcToken and oauth2Token are nil when disabled.
func apiAuthenticatorChain(apiKey authn.Authenticator, oidcToken *oidctoken.Handler, oauth2Token *oauth2token.Handler, session authn.Authenticator) []authn.Authenticator {
	var chain []authn.Authenticator
	if oidcToken != nil {
		chain = append(chain, oidcToken)
	}
	chain = append(chain, apiKey)
	if oauth2Token != nil {
		chain = append(chain, oauth2Token)
	}
	return append(chain, session)
}
