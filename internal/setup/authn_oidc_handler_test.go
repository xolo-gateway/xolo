package setup

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markbates/goth"
	"github.com/markbates/goth/providers/openidConnect"
	"github.com/xolo-gateway/xolo/internal/config"
)

func TestOIDCCallbackURL(t *testing.T) {
	for name, baseURL := range map[string]string{
		"without trailing slash": "https://xolo.example.com/gateway",
		"with trailing slash":    "https://xolo.example.com/gateway/",
	} {
		t.Run(name, func(t *testing.T) {
			got := oidcCallbackURL(baseURL, "acme-idp")
			want := "https://xolo.example.com/gateway/auth/oidc/providers/acme-idp/callback"
			if got != want {
				t.Errorf("callback url: got %q, want %q", got, want)
			}
		})
	}
}

func TestBuildOIDCProviderDiscoversOnce(t *testing.T) {
	var requests atomic.Int32

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{
			"issuer":"https://idp.example.com",
			"authorization_endpoint":"https://idp.example.com/authorize",
			"token_endpoint":"https://idp.example.com/token",
			"jwks_uri":"https://idp.example.com/jwks",
			"userinfo_endpoint":"https://idp.example.com/userinfo",
			"end_session_endpoint":"https://idp.example.com/logout"
		}`))
		if err != nil {
			t.Errorf("write discovery response: %v", err)
		}
	}))
	t.Cleanup(discoveryServer.Close)

	factory, _, _, err := buildOIDCProvider(
		context.Background(),
		newOIDCDiscoveryHTTPClient(),
		config.NamedOIDCProvider{
			ID: "acme-idp",
			OIDCProvider: config.OIDCProvider{
				OAuth2Provider: config.OAuth2Provider{
					Key:    "client-id",
					Secret: "client-secret",
				},
				DiscoveryURL: discoveryServer.URL,
			},
		},
	)
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}

	goth.ClearProviders()
	t.Cleanup(goth.ClearProviders)

	if err := buildStartupOIDCProviders(
		map[string]oidcProviderFactory{"acme-idp": factory},
		"https://xolo.example.com",
		false,
	); err != nil {
		t.Fatalf("validate startup provider: %v", err)
	}

	registry := newHostScopedProviders(map[string]oidcProviderFactory{"acme-idp": factory})
	for _, baseURL := range []string{
		"https://one.xolo.example.com",
		"https://two.xolo.example.com",
	} {
		if _, err := registry.Resolve("acme-idp", baseURL); err != nil {
			t.Fatalf("resolve provider for %q: %v", baseURL, err)
		}
	}

	if got := requests.Load(); got != 1 {
		t.Errorf("discovery requests: got %d, want 1", got)
	}
}

func TestBuildOIDCProviderRejectsInvalidDiscovery(t *testing.T) {
	for name, testCase := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"non-success status": {
			status: http.StatusBadGateway,
			body:   `{}`,
			want:   "status 502",
		},
		"malformed document": {
			status: http.StatusOK,
			body:   `{`,
			want:   "unexpected EOF",
		},
	} {
		t.Run(name, func(t *testing.T) {
			discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
				_, err := w.Write([]byte(testCase.body))
				if err != nil {
					t.Errorf("write discovery response: %v", err)
				}
			}))
			t.Cleanup(discoveryServer.Close)

			_, _, _, err := buildOIDCProvider(
				context.Background(),
				newOIDCDiscoveryHTTPClient(),
				config.NamedOIDCProvider{
					ID: "broken",
					OIDCProvider: config.OIDCProvider{
						OAuth2Provider: config.OAuth2Provider{
							Key:    "client-id",
							Secret: "client-secret",
						},
						DiscoveryURL: discoveryServer.URL,
					},
				},
			)
			if err == nil {
				t.Fatalf("error: got nil, want one containing %q", testCase.want)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error: got %q, want one containing %q", err, testCase.want)
			}
		})
	}

	// The three endpoints that remain mandatory after the jwks_uri relaxation
	// (issuer, authorization_endpoint, token_endpoint) are exercised below:
	// each is asserted both for being absent and for being present but not an
	// absolute http(s) URL.
	for _, endpoint := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing issuer",
			body: `{
				"authorization_endpoint":"https://idp.example.com/authorize",
				"token_endpoint":"https://idp.example.com/token",
				"jwks_uri":"https://idp.example.com/jwks"
			}`,
			want: `missing "issuer"`,
		},
		{
			name: "missing authorization_endpoint",
			body: `{"issuer":"https://idp.example.com"}`,
			want: `missing "authorization_endpoint"`,
		},
		{
			name: "missing token_endpoint",
			body: `{
				"issuer":"https://idp.example.com",
				"authorization_endpoint":"https://idp.example.com/authorize",
				"jwks_uri":"https://idp.example.com/jwks"
			}`,
			want: `missing "token_endpoint"`,
		},
		{
			name: "relative issuer",
			body: `{
				"issuer":"/issuer",
				"authorization_endpoint":"https://idp.example.com/authorize",
				"token_endpoint":"https://idp.example.com/token",
				"jwks_uri":"https://idp.example.com/jwks"
			}`,
			want: `field "issuer" must be an absolute`,
		},
		{
			name: "relative authorization_endpoint",
			body: `{
				"issuer":"https://idp.example.com",
				"authorization_endpoint":"/authorize",
				"token_endpoint":"https://idp.example.com/token",
				"jwks_uri":"https://idp.example.com/jwks"
			}`,
			want: `field "authorization_endpoint" must be an absolute`,
		},
		{
			name: "relative token_endpoint",
			body: `{
				"issuer":"https://idp.example.com",
				"authorization_endpoint":"https://idp.example.com/authorize",
				"token_endpoint":"/token",
				"jwks_uri":"https://idp.example.com/jwks"
			}`,
			want: `field "token_endpoint" must be an absolute`,
		},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(endpoint.body))
				if err != nil {
					t.Errorf("write discovery response: %v", err)
				}
			}))
			t.Cleanup(discoveryServer.Close)

			_, _, _, err := buildOIDCProvider(
				context.Background(),
				newOIDCDiscoveryHTTPClient(),
				config.NamedOIDCProvider{
					ID: "broken",
					OIDCProvider: config.OIDCProvider{
						OAuth2Provider: config.OAuth2Provider{
							Key:    "client-id",
							Secret: "client-secret",
						},
						DiscoveryURL: discoveryServer.URL,
					},
				},
			)
			if err == nil {
				t.Fatalf("error: got nil, want one containing %q", endpoint.want)
			}
			if !strings.Contains(err.Error(), endpoint.want) {
				t.Errorf("error: got %q, want one containing %q", err, endpoint.want)
			}
		})
	}
}

// TestBuildOIDCProviderToleratesMissingJWKSURI asserts the relaxed policy: a
// discovery document missing jwks_uri must boot, leave the interactive-login
// factory usable, and skip the JWKS registry so the API-token authenticator
// ignores the provider. The companion TestBuildOIDCProviderRejectsInvalidDiscovery
// still covers the three endpoints that remain mandatory.
func TestBuildOIDCProviderToleratesMissingJWKSURI(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{
			"issuer":"https://idp.example.com",
			"authorization_endpoint":"https://idp.example.com/authorize",
			"token_endpoint":"https://idp.example.com/token",
			"userinfo_endpoint":"https://idp.example.com/userinfo"
		}`))
		if err != nil {
			t.Errorf("write discovery response: %v", err)
		}
	}))
	t.Cleanup(discoveryServer.Close)

	factory, provider, withJWKS, err := buildOIDCProvider(
		context.Background(),
		newOIDCDiscoveryHTTPClient(),
		config.NamedOIDCProvider{
			ID: "opaque-idp",
			OIDCProvider: config.OIDCProvider{
				OAuth2Provider: config.OAuth2Provider{
					Key:    "client-id",
					Secret: "client-secret",
				},
				DiscoveryURL: discoveryServer.URL,
			},
		},
	)
	if err != nil {
		t.Fatalf("build provider: got error %v, want nil", err)
	}
	if factory == nil {
		t.Fatalf("factory: got nil, want a non-nil goth provider factory")
	}
	if provider.ID != "opaque-idp" {
		t.Errorf("provider id: got %q, want %q", provider.ID, "opaque-idp")
	}
	if withJWKS != nil {
		t.Errorf("withJWKS: got %+v, want nil so the JWKS-based authenticator ignores the provider", withJWKS)
	}

	goth.ClearProviders()
	t.Cleanup(goth.ClearProviders)

	if err := buildStartupOIDCProviders(
		map[string]oidcProviderFactory{"opaque-idp": factory},
		"https://xolo.example.com",
		false,
	); err != nil {
		t.Fatalf("validate startup provider: %v", err)
	}

	logsStr := logs.String()
	if got := strings.Count(logsStr, "oidc provider discovery document has no jwks_uri"); got != 1 {
		t.Errorf("jwks_uri warning count: got %d, want 1; logs: %s", got, logsStr)
	}
	if !strings.Contains(logsStr, "provider=opaque-idp") {
		t.Errorf("warning missing provider attribute; logs: %s", logsStr)
	}
	if !strings.Contains(logsStr, "discovery_url="+discoveryServer.URL) {
		t.Errorf("warning missing discovery_url attribute; logs: %s", logsStr)
	}
}

// TestBuildGiteaProviderToleratesMissingJWKSURI asserts the Gitea branch of
// buildGiteaProvider behaves like buildOIDCProvider: a discovery document
// missing jwks_uri must boot, leave the interactive-login factory usable, skip
// the JWKS registry, and emit a single warning naming the affected provider.
func TestBuildGiteaProviderToleratesMissingJWKSURI(t *testing.T) {
	t.Run("missing jwks_uri logs a warning", func(t *testing.T) {
		var logs bytes.Buffer
		previousLogger := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
		t.Cleanup(func() { slog.SetDefault(previousLogger) })

		discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{
				"issuer":"https://gitea.example.com",
				"authorization_endpoint":"https://gitea.example.com/login/oauth/authorize",
				"token_endpoint":"https://gitea.example.com/login/oauth/access_token",
				"userinfo_endpoint":"https://gitea.example.com/login/oauth/userinfo"
			}`))
			if err != nil {
				t.Errorf("write discovery response: %v", err)
			}
		}))
		t.Cleanup(discoveryServer.Close)

		factory, provider, withJWKS, err := buildGiteaProvider(
			context.Background(),
			newOIDCDiscoveryHTTPClient(),
			config.GiteaProvider{
				OAuth2Provider: config.OAuth2Provider{
					Key:    "client-id",
					Secret: "client-secret",
				},
				DiscoveryURL: discoveryServer.URL,
				Label:        "Gitea",
			},
		)
		if err != nil {
			t.Fatalf("build provider: got error %v, want nil", err)
		}
		if factory == nil {
			t.Fatalf("factory: got nil, want a non-nil goth provider factory")
		}
		if provider.ID != "gitea" {
			t.Errorf("provider id: got %q, want %q", provider.ID, "gitea")
		}
		if withJWKS != nil {
			t.Errorf("withJWKS: got %+v, want nil so the JWKS-based authenticator ignores the provider", withJWKS)
		}

		logsStr := logs.String()
		if got := strings.Count(logsStr, "gitea provider discovery document has no jwks_uri"); got != 1 {
			t.Errorf("jwks_uri warning count: got %d, want 1; logs: %s", got, logsStr)
		}
		if !strings.Contains(logsStr, "provider=gitea") {
			t.Errorf("warning missing provider attribute; logs: %s", logsStr)
		}
		if !strings.Contains(logsStr, "discovery_url="+discoveryServer.URL) {
			t.Errorf("warning missing discovery_url attribute; logs: %s", logsStr)
		}
	})

	t.Run("jwks_uri present populates the JWKS registry", func(t *testing.T) {
		discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{
				"issuer":"https://gitea.example.com",
				"authorization_endpoint":"https://gitea.example.com/login/oauth/authorize",
				"token_endpoint":"https://gitea.example.com/login/oauth/access_token",
				"jwks_uri":"https://gitea.example.com/login/oauth/keys"
			}`))
			if err != nil {
				t.Errorf("write discovery response: %v", err)
			}
		}))
		t.Cleanup(discoveryServer.Close)

		_, _, withJWKS, err := buildGiteaProvider(
			context.Background(),
			newOIDCDiscoveryHTTPClient(),
			config.GiteaProvider{
				OAuth2Provider: config.OAuth2Provider{
					Key:    "client-id",
					Secret: "client-secret",
				},
				DiscoveryURL: discoveryServer.URL,
				Label:        "Gitea",
			},
		)
		if err != nil {
			t.Fatalf("build provider: got error %v, want nil", err)
		}
		if withJWKS == nil {
			t.Fatal("withJWKS: got nil, want a populated descriptor")
		}
		if withJWKS.JWKSURL != "https://gitea.example.com/login/oauth/keys" {
			t.Errorf("jwks url: got %q, want the discovered value", withJWKS.JWKSURL)
		}
		if withJWKS.Issuer != "https://gitea.example.com" {
			t.Errorf("issuer: got %q, want the discovered value", withJWKS.Issuer)
		}
	})
}

// TestBuildGiteaProviderRejectsInvalidDiscovery asserts the Gitea branch
// applies validateOIDCDiscovery on par with buildOIDCProvider. Mirrors
// TestBuildOIDCProviderRejectsInvalidDiscovery: a non-success status, a
// malformed body, a missing mandatory endpoint, or a relative URL all fail
// boot.
func TestBuildGiteaProviderRejectsInvalidDiscovery(t *testing.T) {
	t.Run("missing discovery url is required", func(t *testing.T) {
		_, _, _, err := buildGiteaProvider(
			context.Background(),
			newOIDCDiscoveryHTTPClient(),
			config.GiteaProvider{
				OAuth2Provider: config.OAuth2Provider{
					Key:    "client-id",
					Secret: "client-secret",
				},
				DiscoveryURL: "",
				Label:        "Gitea",
			},
		)
		if err == nil {
			t.Fatal("error: got nil, want an error requiring a discovery url")
		}
		if !strings.Contains(err.Error(), "discovery url is required") {
			t.Errorf("error: got %q, want one containing %q", err, "discovery url is required")
		}
	})

	for name, testCase := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"non-success status": {
			status: http.StatusBadGateway,
			body:   `{}`,
			want:   "status 502",
		},
		"malformed document": {
			status: http.StatusOK,
			body:   `{`,
			want:   "unexpected EOF",
		},
		"missing required endpoint": {
			status: http.StatusOK,
			body:   `{"issuer":"https://gitea.example.com"}`,
			want:   `missing "authorization_endpoint"`,
		},
		"relative required endpoint": {
			status: http.StatusOK,
			body: `{
				"issuer":"https://gitea.example.com",
				"authorization_endpoint":"/authorize",
				"token_endpoint":"https://gitea.example.com/login/oauth/access_token",
				"jwks_uri":"https://gitea.example.com/login/oauth/keys"
			}`,
			want: `field "authorization_endpoint" must be an absolute`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
				_, err := w.Write([]byte(testCase.body))
				if err != nil {
					t.Errorf("write discovery response: %v", err)
				}
			}))
			t.Cleanup(discoveryServer.Close)

			_, _, _, err := buildGiteaProvider(
				context.Background(),
				newOIDCDiscoveryHTTPClient(),
				config.GiteaProvider{
					OAuth2Provider: config.OAuth2Provider{
						Key:    "client-id",
						Secret: "client-secret",
					},
					DiscoveryURL: discoveryServer.URL,
					Label:        "Gitea",
				},
			)
			if err == nil {
				t.Fatalf("error: got nil, want one containing %q", testCase.want)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error: got %q, want one containing %q", err, testCase.want)
			}
		})
	}

	// Like TestBuildOIDCProviderRejectsInvalidDiscovery, exercise each of the
	// three mandatory endpoints (issuer, authorization_endpoint,
	// token_endpoint) for both the missing and the relative-URL cases.
	for _, endpoint := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing issuer",
			body: `{
				"authorization_endpoint":"https://gitea.example.com/login/oauth/authorize",
				"token_endpoint":"https://gitea.example.com/login/oauth/access_token",
				"jwks_uri":"https://gitea.example.com/login/oauth/keys"
			}`,
			want: `missing "issuer"`,
		},
		{
			name: "missing token_endpoint",
			body: `{
				"issuer":"https://gitea.example.com",
				"authorization_endpoint":"https://gitea.example.com/login/oauth/authorize",
				"jwks_uri":"https://gitea.example.com/login/oauth/keys"
			}`,
			want: `missing "token_endpoint"`,
		},
		{
			name: "relative issuer",
			body: `{
				"issuer":"/issuer",
				"authorization_endpoint":"https://gitea.example.com/login/oauth/authorize",
				"token_endpoint":"https://gitea.example.com/login/oauth/access_token",
				"jwks_uri":"https://gitea.example.com/login/oauth/keys"
			}`,
			want: `field "issuer" must be an absolute`,
		},
		{
			name: "relative token_endpoint",
			body: `{
				"issuer":"https://gitea.example.com",
				"authorization_endpoint":"https://gitea.example.com/login/oauth/authorize",
				"token_endpoint":"/token",
				"jwks_uri":"https://gitea.example.com/login/oauth/keys"
			}`,
			want: `field "token_endpoint" must be an absolute`,
		},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(endpoint.body))
				if err != nil {
					t.Errorf("write discovery response: %v", err)
				}
			}))
			t.Cleanup(discoveryServer.Close)

			_, _, _, err := buildGiteaProvider(
				context.Background(),
				newOIDCDiscoveryHTTPClient(),
				config.GiteaProvider{
					OAuth2Provider: config.OAuth2Provider{
						Key:    "client-id",
						Secret: "client-secret",
					},
					DiscoveryURL: discoveryServer.URL,
					Label:        "Gitea",
				},
			)
			if err == nil {
				t.Fatalf("error: got nil, want one containing %q", endpoint.want)
			}
			if !strings.Contains(err.Error(), endpoint.want) {
				t.Errorf("error: got %q, want one containing %q", err, endpoint.want)
			}
		})
	}
}

func TestOIDCDiscoveryClientTimeout(t *testing.T) {
	if got := newOIDCDiscoveryHTTPClient().Timeout; got != 10*time.Second {
		t.Fatalf("production timeout: got %s, want %s", got, 10*time.Second)
	}

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(discoveryServer.Close)

	client := &http.Client{Timeout: 20 * time.Millisecond}
	started := time.Now()
	_, err := fetchOIDCDiscovery(context.Background(), client, discoveryServer.URL)
	if err == nil {
		t.Fatal("error: got nil, want a timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error: got %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("timeout was not bounded: request took %s", elapsed)
	}
}

func TestBuildStartupOIDCProvidersRegistration(t *testing.T) {
	newFactory := func(built *int, instance **stubGothProvider) oidcProviderFactory {
		return func(callbackURL string) (goth.Provider, error) {
			*built++
			provider := &stubGothProvider{callbackURL: callbackURL}
			*instance = provider
			return provider, nil
		}
	}

	t.Run("multi-tenant validates without registering", func(t *testing.T) {
		goth.ClearProviders()
		t.Cleanup(goth.ClearProviders)

		built := 0
		var instance *stubGothProvider
		err := buildStartupOIDCProviders(
			map[string]oidcProviderFactory{"acme-idp": newFactory(&built, &instance)},
			"https://xolo.example.com",
			false,
		)
		if err != nil {
			t.Fatalf("startup validation: %v", err)
		}
		if built != 1 {
			t.Errorf("providers built: got %d, want 1", built)
		}
		if _, err := goth.GetProvider("acme-idp"); err == nil {
			t.Fatal("provider was registered in multi-tenant mode")
		}
	})

	t.Run("single-tenant registers the validated instance", func(t *testing.T) {
		goth.ClearProviders()
		t.Cleanup(goth.ClearProviders)

		built := 0
		var instance *stubGothProvider
		err := buildStartupOIDCProviders(
			map[string]oidcProviderFactory{"acme-idp": newFactory(&built, &instance)},
			"https://xolo.example.com/",
			true,
		)
		if err != nil {
			t.Fatalf("startup registration: %v", err)
		}
		if built != 1 {
			t.Errorf("providers built: got %d, want 1", built)
		}

		registered, err := goth.GetProvider("acme-idp")
		if err != nil {
			t.Fatalf("get registered provider: %v", err)
		}
		if registered != instance {
			t.Errorf("registered provider: got %p, want validated instance %p", registered, instance)
		}
		if want := "https://xolo.example.com/auth/oidc/providers/acme-idp/callback"; instance.callbackURL != want {
			t.Errorf("callback url: got %q, want %q", instance.callbackURL, want)
		}
	})
}

func TestBuildOIDCProviderUsesDiscoveredEndpoints(t *testing.T) {
	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{
			"issuer":"https://idp.example.com",
			"authorization_endpoint":"https://idp.example.com/authorize",
			"token_endpoint":"https://idp.example.com/token",
			"jwks_uri":"https://idp.example.com/jwks"
		}`))
		if err != nil {
			t.Errorf("write discovery response: %v", err)
		}
	}))
	t.Cleanup(discoveryServer.Close)

	factory, _, _, err := buildOIDCProvider(
		context.Background(),
		newOIDCDiscoveryHTTPClient(),
		config.NamedOIDCProvider{
			ID: "acme-idp",
			OIDCProvider: config.OIDCProvider{
				OAuth2Provider: config.OAuth2Provider{Key: "key", Secret: "secret"},
				DiscoveryURL:   discoveryServer.URL,
			},
		},
	)
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}

	provider, err := factory("https://acme.xolo.example.com/callback")
	if err != nil {
		t.Fatalf("instantiate provider: %v", err)
	}
	custom, ok := provider.(*openidConnect.Provider)
	if !ok {
		t.Fatalf("provider: got %T, want *openidConnect.Provider", provider)
	}
	if got := custom.OpenIDConfig.AuthEndpoint; got != "https://idp.example.com/authorize" {
		t.Errorf("authorization endpoint: got %q", got)
	}
	if got := custom.OpenIDConfig.TokenEndpoint; got != "https://idp.example.com/token" {
		t.Errorf("token endpoint: got %q", got)
	}
}
