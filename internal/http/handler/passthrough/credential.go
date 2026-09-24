package passthrough

import (
	"context"
	"net/http"
	"strings"
)

type contextKey string

const contextKeyUpstreamCredential contextKey = "upstreamCredential"

// CredentialSwap moves the Xolo API token from credentialHeader into
// Authorization, and stashes the value the client put in Authorization — its
// upstream credential — in the request context.
//
// The swap exists because the two credentials compete for one header. Anthropic
// SDK clients put their own credential in Authorization and offer no way to
// relocate it, so the Xolo token has to travel in a header of its own and be
// put back before authentication runs. Doing the swap here lets the whole
// existing chain (authn, bridge, active check, memberships) run unmodified, and
// keeps the upstream credential out of every component but the relay itself.
//
// It must be mounted outside the authn chain.
func CredentialSwap(credentialHeader string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upstreamCredential := r.Header.Get("Authorization")

			if xoloToken := r.Header.Get(credentialHeader); xoloToken != "" {
				r.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(xoloToken, "Bearer "))
				r.Header.Del(credentialHeader)
			} else {
				// No Xolo token: leave Authorization untouched so the authn chain
				// sees the request exactly as it arrived and rejects it on its own
				// terms. Relaying an unidentified call is never correct here.
				r.Header.Del("Authorization")
			}

			if upstreamCredential != "" {
				r = r.WithContext(context.WithValue(r.Context(), contextKeyUpstreamCredential, upstreamCredential))
			}

			next.ServeHTTP(w, r)
		})
	}
}

// UpstreamCredentialFromContext returns the credential the client supplied, to
// be replayed verbatim upstream. Empty when the client sent none.
func UpstreamCredentialFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyUpstreamCredential).(string)
	return v
}
