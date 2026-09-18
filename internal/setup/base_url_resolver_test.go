package setup

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/http/middleware/tenant"
)

func TestNewTenantBaseURLResolver(t *testing.T) {
	tenantResolver := tenant.NewResolver(nil, config.Multitenancy{
		Enabled:           true,
		HostPattern:       "{tenant}.XOLO.Example.Com",
		DefaultTenantSlug: model.DefaultTenantSlug,
	}, "")
	canonicalHost := tenantResolver.CanonicalHost

	t.Run("refuses a base url that is not absolute", func(t *testing.T) {
		if _, err := newTenantBaseURLResolver("/", canonicalHost); err == nil {
			t.Fatal("error: got nil, want one")
		}
	})

	for name, testCase := range map[string]struct {
		baseURL string
		host    string
		want    string
	}{
		"tenant host takes over the authority": {
			baseURL: "https://xolo.example.com",
			host:    "AcMe.XOLO.Example.Com",
			want:    "https://acme.xolo.example.com",
		},
		"the configured port replaces the request port": {
			baseURL: "http://xolo.example.com:3002",
			host:    "acme.xolo.example.com:9999",
			want:    "http://acme.xolo.example.com:3002",
		},
		"a request port is ignored when none is configured": {
			baseURL: "https://xolo.example.com",
			host:    "acme.xolo.example.com:9999",
			want:    "https://acme.xolo.example.com",
		},
		"the configured path is kept": {
			baseURL: "https://xolo.example.com:3002/gateway",
			host:    "AcMe.xolo.example.com:9999",
			want:    "https://acme.xolo.example.com:3002/gateway",
		},
		"a host outside the pattern falls back": {
			baseURL: "https://xolo.example.com",
			host:    "evil.example.com",
			want:    "https://xolo.example.com",
		},
		"an empty host falls back": {
			baseURL: "https://xolo.example.com",
			host:    "",
			want:    "https://xolo.example.com",
		},
	} {
		t.Run(name, func(t *testing.T) {
			resolve, err := newTenantBaseURLResolver(testCase.baseURL, canonicalHost)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = testCase.host

			if got := resolve(req); got != testCase.want {
				t.Errorf("base url: got %q, want %q", got, testCase.want)
			}
		})
	}
}
