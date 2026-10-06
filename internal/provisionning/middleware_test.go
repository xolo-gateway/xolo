package provisionning

import (
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

func TestClientPolicyAndCorrelation(t *testing.T) {
	const first = "urn:test:first"
	const second = "urn:test:second"
	var calls int
	handler := requestCorrelation(requireClientCert(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		actor := model.ActorFromContext(r.Context())
		require.Equal(t, w.Header().Get("X-Request-ID"), actor.RequestID)
		require.NotEmpty(t, actor.URI)
	}), []string{first, second}, 0.01, 1))
	request := func(uri string, ids ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/v1/tenants", nil)
		if uri != "" {
			u, _ := url.Parse(uri)
			leaf := &x509.Certificate{SerialNumber: big.NewInt(1), URIs: []*url.URL{u}}
			r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
		}
		for _, id := range ids {
			r.Header.Add("X-Request-ID", id)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		require.Regexp(t, requestIDPattern, rec.Header().Get("X-Request-ID"))
		return rec
	}
	id := strings.Repeat("a", 32)
	require.Equal(t, id, request(first, id).Header().Get("X-Request-ID"))
	limited := request(first, "bad")
	require.Equal(t, 429, limited.Code)
	require.NotEmpty(t, limited.Header().Get("Retry-After"))
	require.Contains(t, limited.Body.String(), "rate_limited")
	require.Equal(t, 200, request(second, id, id).Code)
	require.Equal(t, 2, calls)
	for _, uri := range []string{"", "urn:unknown"} {
		rec := request(uri, strings.Repeat("A", 32))
		require.Equal(t, 403, rec.Code)
		require.Contains(t, rec.Body.String(), "client_certificate_rejected")
	}
	require.Equal(t, 2, calls)
}

func TestRequestIDSelection(t *testing.T) {
	for _, ids := range [][]string{nil, {"bad"}, {strings.Repeat("A", 32)}, {strings.Repeat("a", 32), strings.Repeat("b", 32)}, {strings.Repeat("a", 32) + ", " + strings.Repeat("b", 32)}} {
		r := httptest.NewRequest("GET", "/", nil)
		for _, id := range ids {
			r.Header.Add("X-Request-ID", id)
		}
		rec := httptest.NewRecorder()
		requestCorrelation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Regexp(t, requestIDPattern, r.Header.Get("X-Request-ID"))
		})).ServeHTTP(rec, r)
		require.Regexp(t, requestIDPattern, rec.Header().Get("X-Request-ID"))
		for _, id := range ids {
			require.NotEqual(t, id, rec.Header().Get("X-Request-ID"))
		}
	}
}

func TestClientPolicyWrapsAllRoutes(t *testing.T) {
	for _, target := range []struct{ name, method, path string }{
		{"health", "GET", "/v1/healthz"}, {"permissions", "GET", "/v1/permissions"},
		{"unknown", "GET", "/unknown"}, {"method refused", "TRACE", "/v1/tenants"},
	} {
		t.Run(target.name, func(t *testing.T) {
			called := false
			handler := requestCorrelation(requireClientCert(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), []string{"urn:test:allowed"}, 10, 20))
			for _, state := range []*tls.ConnectionState{nil, {}, {Version: tls.VersionTLS12}} {
				req := httptest.NewRequest(target.method, target.path, nil)
				req.TLS = state
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, req)
				require.Equal(t, http.StatusForbidden, response.Code)
				require.Contains(t, response.Body.String(), "client_certificate_rejected")
				require.Regexp(t, requestIDPattern, response.Header().Get("X-Request-ID"))
				require.False(t, called)
			}
		})
	}
}
