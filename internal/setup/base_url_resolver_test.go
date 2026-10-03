package setup

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewTenantBaseURLResolver(t *testing.T) {
	canonicalHost := func(ctx context.Context, host string) (string, bool) {
		if h, _, e := net.SplitHostPort(host); e == nil {
			host = h
		}
		host = strings.ToLower(host)
		return host, host == "acme.xolo.example.com"
	}

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
		"an unregistered host falls back": {
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
