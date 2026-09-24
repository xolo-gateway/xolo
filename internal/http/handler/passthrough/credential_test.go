package passthrough

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCredentialSwapMovesXoloTokenIntoAuthorization(t *testing.T) {
	var (
		seenAuthorization string
		seenXoloHeader    string
		seenUpstream      string
	)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuthorization = r.Header.Get("Authorization")
		seenXoloHeader = r.Header.Get("X-Xolo-Key")
		seenUpstream = UpstreamCredentialFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-ant-upstream")
	req.Header.Set("X-Xolo-Key", "xolo-token")

	CredentialSwap("X-Xolo-Key")(next).ServeHTTP(httptest.NewRecorder(), req)

	if seenAuthorization != "Bearer xolo-token" {
		t.Errorf("Authorization = %q, want the Xolo token so the authn chain can read it", seenAuthorization)
	}
	if seenUpstream != "Bearer sk-ant-upstream" {
		t.Errorf("upstream credential = %q, want the client's original Authorization", seenUpstream)
	}
	if seenXoloHeader != "" {
		t.Errorf("credential header still set downstream: %q", seenXoloHeader)
	}
}

// Clients may or may not prefix the token; both must authenticate identically.
func TestCredentialSwapNormalizesBearerPrefix(t *testing.T) {
	for name, value := range map[string]string{
		"bare":     "xolo-token",
		"prefixed": "Bearer xolo-token",
	} {
		t.Run(name, func(t *testing.T) {
			var seen string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.Header.Get("Authorization")
			})

			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			req.Header.Set("X-Xolo-Key", value)

			CredentialSwap("X-Xolo-Key")(next).ServeHTTP(httptest.NewRecorder(), req)

			if seen != "Bearer xolo-token" {
				t.Errorf("Authorization = %q, want %q", seen, "Bearer xolo-token")
			}
		})
	}
}

// Without a Xolo token there is no caller to bill. Leaving the upstream
// credential in Authorization would let it reach the authenticators, which
// could only ever reject it — and relaying an unidentified call is never right.
func TestCredentialSwapStripsAuthorizationWhenNoXoloToken(t *testing.T) {
	var (
		seenAuthorization string
		seenUpstream      string
	)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuthorization = r.Header.Get("Authorization")
		seenUpstream = UpstreamCredentialFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-ant-upstream")

	CredentialSwap("X-Xolo-Key")(next).ServeHTTP(httptest.NewRecorder(), req)

	if seenAuthorization != "" {
		t.Errorf("Authorization = %q, want it stripped", seenAuthorization)
	}
	if seenUpstream != "Bearer sk-ant-upstream" {
		t.Errorf("upstream credential = %q, want it preserved for the relay", seenUpstream)
	}
}
