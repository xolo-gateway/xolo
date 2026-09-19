package setup

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markbates/goth"
	"github.com/markbates/goth/providers/openidConnect"
	"github.com/xolo-gateway/xolo/internal/config"
)

// captureHandler is an slog.Handler that records structured records in memory
// instead of formatting them to a buffer. Tests that exercise startup
// warnings rely on it so that:
//
//   - the format of slog's text handler can change without breaking tests;
//   - assertions inspect typed attributes instead of rendered strings.
//
// withCapturedLogs is the canonical producer of a captureHandler: it builds
// a fresh handler per test and hands it back so the test can pass
// slog.New(capture) directly to the builder under test, leaving
// process-global state untouched. Production code paths, however, log
// through slog.Default() at startup, so any parallel test that captures
// by swapping slog.Default still races against concurrent writes.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
	// attrs is the cumulative set of slog.Attr attached via WithAttrs / slog.Logger.With.
	// group is the active group prefix set via WithGroup / slog.Logger.WithGroup.
	// They are appended to every record captured by Handle so tests can assert
	// on attributes added through the With/WithGroup API surface, not only
	// the per-call attrs.
	attrs []slog.Attr
	group string
}

func newCaptureHandler() *captureHandler {
	return &captureHandler{}
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.attrs) > 0 {
		if h.group != "" {
			// WithGroup prefixes subsequent attrs: surface them as a single
			// grouped attribute so a test inspecting r.Attrs() sees the
			// prefix honoured rather than flat attrs at the top level.
			r.AddAttrs(slog.Attr{Key: h.group, Value: slog.GroupValue(h.attrs...)})
		} else {
			for _, a := range h.attrs {
				r.AddAttrs(a)
			}
		}
	}
	h.records = append(h.records, r)
	return nil
}

// WithAttrs returns a derived handler that carries the supplied attrs in
// addition to the receiver's. They are surfaced in Handle so tests can assert
// on attributes that were attached via slog.Default().With(...).
func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &captureHandler{attrs: merged, group: h.group}
}

// WithGroup returns a derived handler that prefixes subsequent attrs with
// group. Grouped attrs are surfaced by Handle so a future caller doing
// slog.Default().WithGroup("g").Info("...") sees the prefix in assertion.
func (h *captureHandler) WithGroup(name string) slog.Handler {
	return &captureHandler{attrs: h.attrs, group: name}
}

func (h *captureHandler) recordsCopy() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

// attributeValue returns the string value of an attribute on any record whose
// message contains the supplied substring, or ("", false) when none matches.
// It exists to assert on the structured payload of a startup warning without
// rendering it.
func (h *captureHandler) attributeValue(messageSubstr, attrKey string) (string, bool) {
	for _, r := range h.recordsCopy() {
		if !strings.Contains(r.Message, messageSubstr) {
			continue
		}
		var found bool
		var value string
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == attrKey {
				value = a.Value.String()
				found = true
				return false
			}
			return true
		})
		if found {
			return value, true
		}
	}
	return "", false
}

// firstMessage returns the message of the first record whose message
// contains the supplied substring, or ("", false) when none matches.
// Tests use it to verify the message text itself (e.g. the
// "gitea provider" vs "oidc provider" prefix) without rendering it.
func (h *captureHandler) firstMessage(messageSubstr string) (string, bool) {
	for _, r := range h.recordsCopy() {
		if strings.Contains(r.Message, messageSubstr) {
			return r.Message, true
		}
	}
	return "", false
}

// warningCount returns the number of emitted records whose message contains
// the given substring at slog.Level >= slog.LevelWarn.
func (h *captureHandler) warningCount(messageSubstr string) int {
	count := 0
	for _, r := range h.recordsCopy() {
		if r.Level >= slog.LevelWarn && strings.Contains(r.Message, messageSubstr) {
			count++
		}
	}
	return count
}

// withCapturedLogs runs fn with a *slog.Logger that records every record
// through a captureHandler. Tests pass this logger explicitly to the
// builder under test (buildGiteaProvider / buildOIDCProvider) instead of
// relying on slog.Default(), so no process-global state is mutated and
// the helper is safe under t.Parallel.
//
// Usage:
//
//	withCapturedLogs(t, func(c *captureHandler) {
//	    _, _, _, err := buildGiteaProvider(ctx, slog.New(c), client, gp)
//	    ...
//	})
func withCapturedLogs(t *testing.T, fn func(h *captureHandler)) {
	t.Helper()

	capture := newCaptureHandler()
	fn(capture)
}

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
		slog.Default(),
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
				slog.Default(),
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
				slog.Default(),
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

// TestBuildOIDCProviderRejectsMalformedDiscoveryURL pins the M1 fix:
// requireAbsoluteHTTPURL must gate DISCOVERY_URL on the OIDC side too, so a
// shape-malformed value fails boot with the same operator-facing message
// every adjacent field produces rather than dying inside the http client
// with an "unsupported protocol scheme" diagnostic.
func TestBuildOIDCProviderRejectsMalformedDiscoveryURL(t *testing.T) {
	for name, discoveryURL := range map[string]string{
		"missing scheme":  "idp.example.com/.well-known/openid-configuration",
		"non-http scheme": "file:///etc/openid-configuration",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := buildOIDCProvider(
				context.Background(),
				slog.Default(),
				newOIDCDiscoveryHTTPClient(),
				config.NamedOIDCProvider{
					ID: "malformed-discovery",
					OIDCProvider: config.OIDCProvider{
						OAuth2Provider: config.OAuth2Provider{
							Key:    "client-id",
							Secret: "client-secret",
						},
						DiscoveryURL: discoveryURL,
					},
				},
			)
			if err == nil {
				t.Fatalf("error: got nil, want a startup refusal on malformed DiscoveryURL %q", discoveryURL)
			}
			if !strings.Contains(err.Error(), "DISCOVERY_URL") {
				t.Errorf("error: got %q, want it to mention DISCOVERY_URL", err)
			}
			if !strings.Contains(err.Error(), "absolute http(s) url") {
				t.Errorf("error: got %q, want it to describe the absolute-http(s) shape requirement", err)
			}
		})
	}
}

// TestBuildOIDCProviderToleratesMissingJWKSURI asserts the relaxed policy: a
// discovery document missing jwks_uri must boot, leave the interactive-login
// factory usable, keep the introspection / UserInfo credentials on the
// descriptor (so ProvidersForTokenValidation can still see the provider), and
// emit a single warning naming the affected provider. The companion
// TestBuildOIDCProviderRejectsInvalidDiscovery still covers the three
// endpoints that remain mandatory.
func TestBuildOIDCProviderToleratesMissingJWKSURI(t *testing.T) {
	withCapturedLogs(t, func(logs *captureHandler) {
		discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{
				"issuer":"https://idp.example.com",
				"authorization_endpoint":"https://idp.example.com/authorize",
				"token_endpoint":"https://idp.example.com/token",
				"introspection_endpoint":"https://idp.example.com/introspect",
				"userinfo_endpoint":"https://idp.example.com/userinfo"
			}`))
			if err != nil {
				t.Errorf("write discovery response: %v", err)
			}
		}))
		t.Cleanup(discoveryServer.Close)

		factory, provider, withJWKS, err := buildOIDCProvider(
			context.Background(),
			slog.New(logs),
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
		// The descriptor must remain populated so oauth2token (introspection
		// / UserInfo) keeps working: this is the case from issue #27.
		if withJWKS == nil {
			t.Fatal("withJWKS: got nil, want a populated descriptor so ProvidersForTokenValidation still sees the provider")
		}
		if withJWKS.JWKSURL != "" {
			t.Errorf("jwks url: got %q, want empty so oidctoken skips the provider", withJWKS.JWKSURL)
		}
		if withJWKS.IntrospectionURL != "https://idp.example.com/introspect" {
			t.Errorf("introspection url: got %q, want the discovered value so oauth2token still validates opaque tokens", withJWKS.IntrospectionURL)
		}
		if withJWKS.UserInfoURL != "https://idp.example.com/userinfo" {
			t.Errorf("userinfo url: got %q, want the discovered value as fallback when no introspection is available", withJWKS.UserInfoURL)
		}
		if withJWKS.ClientID != "client-id" || withJWKS.ClientSecret != "client-secret" {
			t.Errorf("client credentials: got %q/%q, want client-id/client-secret so introspection can authenticate", withJWKS.ClientID, withJWKS.ClientSecret)
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

		const warning = "discovery document has no jwks_uri"
		// Assert "at least one" rather than exactly one: a future code path
		// that emits the same warning (e.g. another provider on the same
		// handler) must not silently break this test, and the
		// attribute-level checks below already pin down the affected
		// provider.
		if got := logs.warningCount(warning); got < 1 {
			t.Errorf("jwks_uri warning count: got %d, want >= 1", got)
		}
		if value, ok := logs.attributeValue(warning, "provider"); !ok || value != "opaque-idp" {
			t.Errorf("warning provider attribute: got (%q, %t), want (\"opaque-idp\", true)", value, ok)
		}
		if value, ok := logs.attributeValue(warning, "discovery_url"); !ok || value != discoveryServer.URL {
			t.Errorf("warning discovery_url attribute: got (%q, %t), want %q", value, ok, discoveryServer.URL)
		}
	})
}

// TestBuildOIDCProviderToleratesUnusableJWKSURI asserts the relaxed policy is
// applied uniformly to an unusable (relative, missing scheme, host-less) value:
// the operator gets one warning and the provider stays available for
// introspection / UserInfo. This is the same family of non-conformance that
// issue #27 targets.
func TestBuildOIDCProviderToleratesUnusableJWKSURI(t *testing.T) {
	for name, body := range map[string]string{
		"relative jwks_uri": `{
			"issuer":"https://idp.example.com",
			"authorization_endpoint":"https://idp.example.com/authorize",
			"token_endpoint":"https://idp.example.com/token",
			"jwks_uri":"/.well-known/jwks",
			"introspection_endpoint":"https://idp.example.com/introspect"
		}`,
		"non-http scheme": `{
			"issuer":"https://idp.example.com",
			"authorization_endpoint":"https://idp.example.com/authorize",
			"token_endpoint":"https://idp.example.com/token",
			"jwks_uri":"file:///etc/jwks.json",
			"introspection_endpoint":"https://idp.example.com/introspect"
		}`,
	} {
		t.Run(name, func(t *testing.T) {
			withCapturedLogs(t, func(logs *captureHandler) {
				discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, err := w.Write([]byte(body))
					if err != nil {
						t.Errorf("write discovery response: %v", err)
					}
				}))
				t.Cleanup(discoveryServer.Close)

				_, _, withJWKS, err := buildOIDCProvider(
					context.Background(),
					slog.New(logs),
					newOIDCDiscoveryHTTPClient(),
					config.NamedOIDCProvider{
						ID: "broken-jwks",
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
				if withJWKS == nil {
					t.Fatal("withJWKS: got nil, want a populated descriptor so introspection / UserInfo still see this provider")
				}
				if withJWKS.JWKSURL != "" {
					t.Errorf("jwks url: got %q, want empty", withJWKS.JWKSURL)
				}
				if withJWKS.IntrospectionURL == "" {
					t.Error("introspection url: got empty, want the discovered value so oauth2token stays operational")
				}

				const warning = "jwks_uri is not an absolute http(s) url"
				// At-least-one assertion: the attribute-level checks below
				// pin the affected provider, so a future second emission
				// site for the same warning does not silently break this
				// test.
				if got := logs.warningCount(warning); got < 1 {
					t.Errorf("unusable-jwks warning count: got %d, want >= 1", got)
				}
				if value, ok := logs.attributeValue(warning, "provider"); !ok || value != "broken-jwks" {
					t.Errorf("warning provider attribute: got (%q, %t), want (\"broken-jwks\", true)", value, ok)
				}
			})
		})
	}
}

// TestTolerableJWKSURIAcceptsUppercaseScheme locks in RFC 3986
// case-insensitivity for the http(s) scheme comparison: an IdP that publishes
// "HTTPS://..." must not be flagged as having an unusable jwks_uri.
func TestTolerableJWKSURIAcceptsUppercaseScheme(t *testing.T) {
	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{
			"issuer":"https://idp.example.com",
			"authorization_endpoint":"https://idp.example.com/authorize",
			"token_endpoint":"https://idp.example.com/token",
			"jwks_uri":"HTTPS://idp.example.com/jwks",
			"introspection_endpoint":"https://idp.example.com/introspect"
		}`))
		if err != nil {
			t.Errorf("write discovery response: %v", err)
		}
	}))
	t.Cleanup(discoveryServer.Close)

	withCapturedLogs(t, func(logs *captureHandler) {
		_, _, withJWKS, err := buildOIDCProvider(
			context.Background(),
			slog.New(logs),
			newOIDCDiscoveryHTTPClient(),
			config.NamedOIDCProvider{
				ID: "uppercase-scheme",
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
		if withJWKS == nil {
			t.Fatal("withJWKS: got nil, want a populated descriptor")
		}
		if withJWKS.JWKSURL != "HTTPS://idp.example.com/jwks" {
			t.Errorf("jwks url: got %q, want the published value preserved verbatim (only the scheme comparison is normalised)", withJWKS.JWKSURL)
		}
		if logs.warningCount("jwks_uri is not an absolute http(s) url") != 0 {
			t.Errorf("uppercase-scheme warning: expected zero warnings, got at least one")
		}
	})
}

// TestTolerableJWKSURIDoesNotPanicOnUnparseableInput locks in the M1 fix:
// url.Parse can return (nil, err) for inputs containing control characters
// (expressible as \u0001 inside a JSON discovery document). Such a value must
// not panic via a nil-pointer dereference on parsed.Scheme. See
// https://pkg.go.dev/net/url#Parse — the parse succeeds for many odd inputs
// but rejects control characters, percent-encoded or not.
func TestTolerableJWKSURIDoesNotPanicOnUnparseableInput(t *testing.T) {
	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{
			"issuer":"https://idp.example.com",
			"authorization_endpoint":"https://idp.example.com/authorize",
			"token_endpoint":"https://idp.example.com/token",
			"jwks_uri":"https://idp.example.com/jwks\u0001extra",
			"introspection_endpoint":"https://idp.example.com/introspect"
		}`))
		if err != nil {
			t.Errorf("write discovery response: %v", err)
		}
	}))
	t.Cleanup(discoveryServer.Close)

	withCapturedLogs(t, func(logs *captureHandler) {
		_, _, withJWKS, err := buildOIDCProvider(
			context.Background(),
			slog.New(logs),
			newOIDCDiscoveryHTTPClient(),
			config.NamedOIDCProvider{
				ID: "evil-jwks",
				OIDCProvider: config.OIDCProvider{
					OAuth2Provider: config.OAuth2Provider{
						Key:    "client-id",
						Secret: "client-secret",
					},
					DiscoveryURL: discoveryServer.URL,
				},
			},
		)
		// The provider is tolerated (boot is not blocked), but jwks_uri is
		// left empty on the descriptor so oidctoken skips it and the loop
		// continues to introspection / UserInfo.
		if err != nil {
			t.Fatalf("build provider: got error %v, want nil (control char in jwks_uri must be tolerated, not boot-blocking)", err)
		}
		if withJWKS == nil {
			t.Fatal("withJWKS: got nil, want a populated descriptor")
		}
		if withJWKS.JWKSURL != "" {
			t.Errorf("jwks url: got %q, want empty so oidctoken skips the provider", withJWKS.JWKSURL)
		}

		const warning = "jwks_uri is not a parseable url"
		// At-least-one assertion: the attribute-level checks pin the
		// affected provider, so a future second emission site does not
		// silently break this test.
		if got := logs.warningCount(warning); got < 1 {
			t.Errorf("parse-error warning count: got %d, want >= 1", got)
		}
		if value, ok := logs.attributeValue(warning, "provider"); !ok || value != "evil-jwks" {
			t.Errorf("warning provider attribute: got (%q, %t), want (\"evil-jwks\", true)", value, ok)
		}
		if _, ok := logs.attributeValue(warning, "parse_error"); !ok {
			t.Errorf("warning missing parse_error attribute")
		}
		// L2 fix: the parse-error branch now carries the same
		// introspection_endpoint / userinfo_endpoint attrs as the other
		// two warnings so operators can verify whether the IdP actually
		// publishes them on every warning shape.
		if value, ok := logs.attributeValue(warning, "introspection_endpoint"); !ok || value != "https://idp.example.com/introspect" {
			t.Errorf("warning introspection_endpoint attribute: got (%q, %t), want the discovered value", value, ok)
		}
	})
}

// TestCaptureHandlerPreservesWithAttrs exercises the L2 fix: captureHandler
// must propagate attrs attached via WithAttrs / WithGroup. The earlier
// implementation dropped them, surprising any test that pre-built a logger
// with With. Records land in the *derived* handler returned by WithAttrs /
// WithGroup (the receiver keeps its own records slice for tests that use the
// base handler directly).
func TestCaptureHandlerPreservesWithAttrs(t *testing.T) {
	derived := newCaptureHandler().
		WithAttrs([]slog.Attr{slog.String("component", "oidc")}).
		WithGroup("auth")

	if err := derived.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelWarn, "probe", 0)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	// derived is *captureHandler behind the slog.Handler interface; record
	// retrieval must go through the same accessor the rest of the test file
	// uses so this test does not depend on the derived's concrete type.
	type recRecorder interface {
		recordsCopy() []slog.Record
	}
	records := derived.(recRecorder).recordsCopy()
	if len(records) != 1 {
		t.Fatalf("records: got %d, want 1", len(records))
	}

	// Handle wraps cumulative attrs under the active group (slog semantics).
	// Drill into the named group to find the "component" attr.
	var gotComponent, gotMessage string
	var foundGroup bool
	records[0].Attrs(func(a slog.Attr) bool {
		if a.Key != "auth" {
			return true
		}
		foundGroup = true
		for _, inner := range a.Value.Group() {
			if inner.Key == "component" {
				gotComponent = inner.Value.String()
			}
		}
		return false
	})
	gotMessage = records[0].Message

	if gotMessage != "probe" {
		t.Errorf("message: got %q, want \"probe\"", gotMessage)
	}
	if !foundGroup {
		t.Errorf("\"auth\" group attr: not found in record; the WithGroup prefix is not honoured by Handle")
	}
	if gotComponent != "oidc" {
		t.Errorf("component attr inside group: got %q, want \"oidc\"", gotComponent)
	}

	// Sanity check: when WithGroup is not in the chain, attrs land flat.
	flat := newCaptureHandler().WithAttrs([]slog.Attr{slog.String("plain", "yes")})
	if err := flat.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelWarn, "flat", 0)); err != nil {
		t.Fatalf("handle flat: %v", err)
	}
	flatRecords := flat.(recRecorder).recordsCopy()
	var gotPlain string
	flatRecords[0].Attrs(func(a slog.Attr) bool {
		if a.Key == "plain" {
			gotPlain = a.Value.String()
		}
		return true
	})
	if gotPlain != "yes" {
		t.Errorf("flat attr: got %q, want \"yes\" (no group should mean flat)", gotPlain)
	}
}

// TestBuildGiteaProviderToleratesMissingJWKSURI asserts the Gitea branch
// mirrors buildOIDCProvider: a discovery document missing jwks_uri must boot,
// leave the interactive-login factory usable, populate the JWKS registry with
// JWKSURL left empty (so oauth2token/introspection still works), and emit a
// single warning naming the affected provider. Additional subtests cover the
// optional discovery url behaviour and the static-config fallback.
func TestBuildGiteaProviderToleratesMissingJWKSURI(t *testing.T) {
	t.Run("missing jwks_uri keeps the provider for introspection", func(t *testing.T) {
		withCapturedLogs(t, func(logs *captureHandler) {
			discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(`{
					"issuer":"https://gitea.example.com",
					"authorization_endpoint":"https://gitea.example.com/login/oauth/authorize",
					"token_endpoint":"https://gitea.example.com/login/oauth/access_token",
					"introspection_endpoint":"https://gitea.example.com/login/oauth/introspect",
					"userinfo_endpoint":"https://gitea.example.com/login/oauth/userinfo"
				}`))
				if err != nil {
					t.Errorf("write discovery response: %v", err)
				}
			}))
			t.Cleanup(discoveryServer.Close)

			factory, provider, withJWKS, err := buildGiteaProvider(
				context.Background(),
				slog.New(logs),
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
			if withJWKS == nil {
				t.Fatal("withJWKS: got nil, want a populated descriptor so introspection still sees the provider")
			}
			if withJWKS.JWKSURL != "" {
				t.Errorf("jwks url: got %q, want empty so oidctoken skips the provider", withJWKS.JWKSURL)
			}
			if withJWKS.IntrospectionURL != "https://gitea.example.com/login/oauth/introspect" {
				t.Errorf("introspection url: got %q, want the discovered value so oauth2token still validates opaque tokens", withJWKS.IntrospectionURL)
			}
			if withJWKS.UserInfoURL != "https://gitea.example.com/login/oauth/userinfo" {
				t.Errorf("userinfo url: got %q, want the discovered value as fallback when no introspection is available", withJWKS.UserInfoURL)
			}

			const warning = "discovery document has no jwks_uri"
			if got := logs.warningCount(warning); got < 1 {
				t.Errorf("jwks_uri warning count: got %d, want >= 1", got)
			}
			if value, ok := logs.attributeValue(warning, "provider"); !ok || value != "gitea" {
				t.Errorf("warning provider attribute: got (%q, %t), want (\"gitea\", true)", value, ok)
			}
			if value, ok := logs.attributeValue(warning, "discovery_url"); !ok || value != discoveryServer.URL {
				t.Errorf("warning discovery_url attribute: got (%q, %t), want %q", value, ok, discoveryServer.URL)
			}
		})
	})

	t.Run("jwks_uri present populates the JWKS registry", func(t *testing.T) {
		discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{
				"issuer":"https://gitea.example.com",
				"authorization_endpoint":"https://gitea.example.com/login/oauth/authorize",
				"token_endpoint":"https://gitea.example.com/login/oauth/access_token",
				"jwks_uri":"https://gitea.example.com/login/oauth/keys",
				"userinfo_endpoint":"https://gitea.example.com/login/oauth/userinfo"
			}`))
			if err != nil {
				t.Errorf("write discovery response: %v", err)
			}
		}))
		t.Cleanup(discoveryServer.Close)

		_, _, withJWKS, err := buildGiteaProvider(
			context.Background(),
			slog.Default(),
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

	t.Run("missing discovery url boots with the static config and a warning", func(t *testing.T) {
		withCapturedLogs(t, func(logs *captureHandler) {
			_, _, withJWKS, err := buildGiteaProvider(
				context.Background(),
				slog.New(logs),
				newOIDCDiscoveryHTTPClient(),
				config.GiteaProvider{
					OAuth2Provider: config.OAuth2Provider{
						Key:    "client-id",
						Secret: "client-secret",
					},
					DiscoveryURL: "",
					AuthURL:      "https://gitea.example.com/login/oauth/authorize",
					TokenURL:     "https://gitea.example.com/login/oauth/access_token",
					ProfileURL:   "https://gitea.example.com/login/oauth/userinfo",
					Label:        "Gitea",
				},
			)
			if err != nil {
				t.Fatalf("build provider: got error %v, want nil (empty discovery url should boot, not refuse)", err)
			}
			// The empty-DiscoveryURL path returns nil for the JWKS-registry
			// descriptor: every endpoint field would be empty, the entry
			// would be inert for both oidctoken and
			// ProvidersForTokenValidation, and registering it would cost
			// an iteration + a debug log per API token. The call site in
			// getOIDCAuthnHandlerFromConfig skips the append when the
			// descriptor is nil.
			if withJWKS != nil {
				t.Errorf("withJWKS: got %+v, want nil so the inert descriptor is not registered", withJWKS)
			}

			const warning = "gitea provider has no discovery url"
			if got := logs.warningCount(warning); got < 1 {
				t.Errorf("missing-discovery warning count: got %d, want >= 1", got)
			}
			// Pin the structured `provider` attribute so a future regression
			// that drops slog.String("provider", "gitea") on this warning
			// path is caught here, mirroring the jwks-missing subtest.
			if value, ok := logs.attributeValue(warning, "provider"); !ok || value != "gitea" {
				t.Errorf("warning provider attribute: got (%q, %t), want (\"gitea\", true)", value, ok)
			}
		})
	})

	t.Run("discovery document missing authorization_endpoint fails boot", func(t *testing.T) {
		// validateOIDCDiscovery enforces authorization_endpoint and
		// token_endpoint as mandatory on the populated-discovery path: even
		// when the operator has set the static AUTH_URL / TOKEN_URL
		// fallback, the discovery document still wins once DiscoveryURL is
		// configured, so the only way to satisfy this branch is to publish
		// a complete document. Empty-DiscoveryURL fallback is exercised
		// separately.
		discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"issuer":"https://gitea.example.com"}`))
			if err != nil {
				t.Errorf("write discovery response: %v", err)
			}
		}))
		t.Cleanup(discoveryServer.Close)

		_, _, _, err := buildGiteaProvider(
			context.Background(),
			slog.Default(),
			newOIDCDiscoveryHTTPClient(),
			config.GiteaProvider{
				OAuth2Provider: config.OAuth2Provider{
					Key:    "client-id",
					Secret: "client-secret",
				},
				DiscoveryURL: discoveryServer.URL,
				AuthURL:      "https://gitea.example.com/login/oauth/authorize",
				TokenURL:     "https://gitea.example.com/login/oauth/access_token",
				ProfileURL:   "https://gitea.example.com/login/oauth/userinfo",
				Label:        "Gitea",
			},
		)
		if err == nil {
			t.Fatal("error: got nil, want an error from validateOIDCDiscovery on missing authorization_endpoint")
		}
		if !strings.Contains(err.Error(), `missing "authorization_endpoint"`) {
			t.Errorf("error: got %q, want it to mention the missing authorization_endpoint", err)
		}
	})

	t.Run("empty DiscoveryURL with empty AuthURL or TokenURL fails boot", func(t *testing.T) {
		// gitea.NewCustomisedURL returns *Provider only and accepts any URL
		// set, so an operator configuring only KEY/SECRET would otherwise
		// boot a non-functional provider. The early validation refuses this
		// combination at startup.
		for name, gp := range map[string]config.GiteaProvider{
			"missing both": {
				OAuth2Provider: config.OAuth2Provider{Key: "client-id", Secret: "client-secret"},
				Label:          "Gitea",
			},
			"missing AuthURL": {
				OAuth2Provider: config.OAuth2Provider{Key: "client-id", Secret: "client-secret"},
				TokenURL:       "https://gitea.example.com/login/oauth/access_token",
				Label:          "Gitea",
			},
			"missing TokenURL": {
				OAuth2Provider: config.OAuth2Provider{Key: "client-id", Secret: "client-secret"},
				AuthURL:        "https://gitea.example.com/login/oauth/authorize",
				Label:          "Gitea",
			},
		} {
			t.Run(name, func(t *testing.T) {
				_, _, _, err := buildGiteaProvider(
					context.Background(),
					slog.Default(),
					newOIDCDiscoveryHTTPClient(),
					gp,
				)
				if err == nil {
					t.Fatal("error: got nil, want a startup refusal")
				}
				if !strings.Contains(err.Error(), "gitea provider requires either DISCOVERY_URL or both AUTH_URL and TOKEN_URL") {
					t.Errorf("error: got %q, want it to mention the required DiscoveryURL/AuthURL/TokenURL combination", err)
				}
			})
		}
	})

	t.Run("empty DiscoveryURL with malformed AuthURL fails boot", func(t *testing.T) {
		// requireAbsoluteHTTPURL mirrors validateOIDCDiscovery's strict shape
		// on the static-config path so a malformed entry fails boot in the
		// same way as a malformed discovery field.
		_, _, _, err := buildGiteaProvider(
			context.Background(),
			slog.Default(),
			newOIDCDiscoveryHTTPClient(),
			config.GiteaProvider{
				OAuth2Provider: config.OAuth2Provider{
					Key:    "client-id",
					Secret: "client-secret",
				},
				AuthURL:    "/relative-path",
				TokenURL:   "https://gitea.example.com/login/oauth/access_token",
				ProfileURL: "https://gitea.example.com/login/oauth/userinfo",
				Label:      "Gitea",
			},
		)
		if err == nil {
			t.Fatal("error: got nil, want a startup refusal on the malformed static URL")
		}
		if !strings.Contains(err.Error(), "AUTH_URL") {
			t.Errorf("error: got %q, want it to mention AUTH_URL", err)
		}
		if !strings.Contains(err.Error(), "absolute http(s) url") {
			t.Errorf("error: got %q, want it to describe the absolute-http(s) shape requirement", err)
		}
	})

	t.Run("empty DiscoveryURL with malformed TokenURL fails boot", func(t *testing.T) {
		// Companion of the AuthURL subtest: requireAbsoluteHTTPURL must
		// gate TOKEN_URL the same way, so a malformed static value fails
		// boot rather than reaching goth and breaking at first login.
		_, _, _, err := buildGiteaProvider(
			context.Background(),
			slog.Default(),
			newOIDCDiscoveryHTTPClient(),
			config.GiteaProvider{
				OAuth2Provider: config.OAuth2Provider{
					Key:    "client-id",
					Secret: "client-secret",
				},
				AuthURL:    "https://gitea.example.com/login/oauth/authorize",
				TokenURL:   "file:///etc/token",
				ProfileURL: "https://gitea.example.com/login/oauth/userinfo",
				Label:      "Gitea",
			},
		)
		if err == nil {
			t.Fatal("error: got nil, want a startup refusal on the malformed TOKEN_URL")
		}
		if !strings.Contains(err.Error(), "TOKEN_URL") {
			t.Errorf("error: got %q, want it to mention TOKEN_URL", err)
		}
		if !strings.Contains(err.Error(), "absolute http(s) url") {
			t.Errorf("error: got %q, want it to describe the absolute-http(s) shape requirement", err)
		}
	})

	t.Run("empty DiscoveryURL with malformed ProfileURL fails boot", func(t *testing.T) {
		// Companion of the AuthURL/TokenURL subtests: PROFILE_URL is
		// optional but, when set on the static-config path, must also
		// satisfy requireAbsoluteHTTPURL — a malformed value would
		// otherwise reach gitea.NewCustomisedURL and break at first login.
		_, _, _, err := buildGiteaProvider(
			context.Background(),
			slog.Default(),
			newOIDCDiscoveryHTTPClient(),
			config.GiteaProvider{
				OAuth2Provider: config.OAuth2Provider{
					Key:    "client-id",
					Secret: "client-secret",
				},
				AuthURL:    "https://gitea.example.com/login/oauth/authorize",
				TokenURL:   "https://gitea.example.com/login/oauth/access_token",
				ProfileURL: "/relative-path",
				Label:      "Gitea",
			},
		)
		if err == nil {
			t.Fatal("error: got nil, want a startup refusal on the malformed PROFILE_URL")
		}
		if !strings.Contains(err.Error(), "PROFILE_URL") {
			t.Errorf("error: got %q, want it to mention PROFILE_URL", err)
		}
		if !strings.Contains(err.Error(), "absolute http(s) url") {
			t.Errorf("error: got %q, want it to describe the absolute-http(s) shape requirement", err)
		}
	})

	t.Run("unusable jwks_uri is tolerated", func(t *testing.T) {
		withCapturedLogs(t, func(logs *captureHandler) {
			discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(`{
					"issuer":"https://gitea.example.com",
					"authorization_endpoint":"https://gitea.example.com/login/oauth/authorize",
					"token_endpoint":"https://gitea.example.com/login/oauth/access_token",
					"jwks_uri":"file:///etc/jwks.json",
					"userinfo_endpoint":"https://gitea.example.com/login/oauth/userinfo"
				}`))
				if err != nil {
					t.Errorf("write discovery response: %v", err)
				}
			}))
			t.Cleanup(discoveryServer.Close)

			_, _, withJWKS, err := buildGiteaProvider(
				context.Background(),
				slog.New(logs),
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
				t.Fatalf("build provider: got error %v, want nil (unusable jwks_uri should be tolerated, not boot-blocking)", err)
			}
			if withJWKS.JWKSURL != "" {
				t.Errorf("jwks url: got %q, want empty so oidctoken skips the provider", withJWKS.JWKSURL)
			}
			const warning = "jwks_uri is not an absolute http(s) url"
			if got := logs.warningCount(warning); got < 1 {
				t.Errorf("unusable-jwks warning count: got %d, want >= 1", got)
			}
			// Pin the message prefix: Gitea operators grepping for the
			// literal "gitea" in the message text must match this warning,
			// not just on the structured provider attribute.
			if msg, ok := logs.firstMessage(warning); !ok || !strings.HasPrefix(msg, "gitea provider ") {
				t.Errorf("warning message prefix: got %q (found=%t), want it to start with %q", msg, ok, "gitea provider ")
			}
		})
	})

	t.Run("malformed DiscoveryURL fails boot", func(t *testing.T) {
		// requireAbsoluteHTTPURL on the discovery-populated path: a value
		// without a scheme reaches fetchOIDCDiscovery and dies inside
		// http.NewRequestWithContext with an opaque "unsupported protocol
		// scheme" diagnostic. The boot-time check turns that into the same
		// clear message every adjacent field produces.
		_, _, _, err := buildGiteaProvider(
			context.Background(),
			slog.Default(),
			newOIDCDiscoveryHTTPClient(),
			config.GiteaProvider{
				OAuth2Provider: config.OAuth2Provider{
					Key:    "client-id",
					Secret: "client-secret",
				},
				DiscoveryURL: "gitea.example.com/.well-known/openid-configuration",
				Label:        "Gitea",
			},
		)
		if err == nil {
			t.Fatal("error: got nil, want a startup refusal on the malformed DiscoveryURL")
		}
		if !strings.Contains(err.Error(), "DISCOVERY_URL") {
			t.Errorf("error: got %q, want it to mention DISCOVERY_URL", err)
		}
		if !strings.Contains(err.Error(), "absolute http(s) url") {
			t.Errorf("error: got %q, want it to describe the absolute-http(s) shape requirement", err)
		}
	})

	t.Run("unparseable DiscoveryURL fails boot with the uniform message", func(t *testing.T) {
		// L1: the parse-error branch of requireAbsoluteHTTPURL must emit
		// the same uniform "<FIELD> must be an absolute http(s) url"
		// message as the scheme/host branches, with the underlying parse
		// error wrapped as the cause. Pin the contract here so a future
		// edit that reverts to a separate "is not a valid url" wording
		// is caught.
		// url.Parse rejects strings containing control characters (see
		// https://pkg.go.dev/net/url#Parse); the embedded 0x01 triggers
		// a parse failure with the same shape as
		// TestTolerableJWKSURIDoesNotPanicOnUnparseableInput's fixture
		// for jwks_uri, just for the DISCOVERY_URL itself.
		_, _, _, err := buildGiteaProvider(
			context.Background(),
			slog.Default(),
			newOIDCDiscoveryHTTPClient(),
			config.GiteaProvider{
				OAuth2Provider: config.OAuth2Provider{
					Key:    "client-id",
					Secret: "client-secret",
				},
				DiscoveryURL: "https://gitea.example.com/.well-known/openid-configuration\x01extra",
				Label:        "Gitea",
			},
		)
		if err == nil {
			t.Fatal("error: got nil, want a startup refusal on the unparseable DiscoveryURL")
		}
		if !strings.Contains(err.Error(), "DISCOVERY_URL") {
			t.Errorf("error: got %q, want it to mention DISCOVERY_URL", err)
		}
		if !strings.Contains(err.Error(), "must be an absolute http(s) url") {
			t.Errorf("error: got %q, want the uniform %q message", err, "must be an absolute http(s) url")
		}
	})

	t.Run("discovery path with no PROFILE_URL and no userinfo_endpoint fails boot", func(t *testing.T) {
		// L3: when neither the static PROFILE_URL nor the discovery
		// document's userinfo_endpoint is set, the factory would receive
		// an empty profile URL and the failure would only surface at
		// first login. Boot must refuse with a clear message so an
		// operator sees the misconfiguration immediately.
		discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{
				"issuer":"https://gitea.example.com",
				"authorization_endpoint":"https://gitea.example.com/login/oauth/authorize",
				"token_endpoint":"https://gitea.example.com/login/oauth/access_token"
			}`))
			if err != nil {
				t.Errorf("write discovery response: %v", err)
			}
		}))
		t.Cleanup(discoveryServer.Close)

		_, _, _, err := buildGiteaProvider(
			context.Background(),
			slog.Default(),
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
			t.Fatal("error: got nil, want a startup refusal when no profile endpoint is available")
		}
		if !strings.Contains(err.Error(), "PROFILE_URL") || !strings.Contains(err.Error(), "userinfo_endpoint") {
			t.Errorf("error: got %q, want it to mention both PROFILE_URL and userinfo_endpoint", err)
		}
	})
}

// TestBuildGiteaProviderRejectsInvalidDiscovery asserts the Gitea branch
// applies validateOIDCDiscovery on par with buildOIDCProvider. Mirrors
// TestBuildOIDCProviderRejectsInvalidDiscovery: a non-success status, a
// malformed body, a missing mandatory endpoint, or a relative URL all fail
// boot.
func TestBuildGiteaProviderRejectsInvalidDiscovery(t *testing.T) {
	// buildGiteaProvider now tolerates an empty DiscoveryURL (the operator
	// gets a startup warning and interactive login falls back to the static
	// AUTH_URL / TOKEN_URL configuration): see
	// TestBuildGiteaProviderToleratesMissingJWKSURI's "missing discovery url
	// boots with the static config and a warning" subtest.

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
				slog.Default(),
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
				slog.Default(),
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

	// M2: when discovery is configured but the operator overrides the
	// profile endpoint with a malformed value, that value must fail
	// boot in the same way as the static-config path. Without this
	// check the failure only surfaces at first profile fetch, as an
	// opaque goth error. Co-located here with the other
	// invalid-discovery cases so all of them live under one test
	// function.
	t.Run("discovery path with malformed PROFILE_URL fails boot", func(t *testing.T) {
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

		_, _, _, err := buildGiteaProvider(
			context.Background(),
			slog.Default(),
			newOIDCDiscoveryHTTPClient(),
			config.GiteaProvider{
				OAuth2Provider: config.OAuth2Provider{
					Key:    "client-id",
					Secret: "client-secret",
				},
				DiscoveryURL: discoveryServer.URL,
				ProfileURL:   "/relative-path",
				Label:        "Gitea",
			},
		)
		if err == nil {
			t.Fatal("error: got nil, want a startup refusal on the malformed PROFILE_URL override on the discovery path")
		}
		if !strings.Contains(err.Error(), "PROFILE_URL") {
			t.Errorf("error: got %q, want it to mention PROFILE_URL", err)
		}
		if !strings.Contains(err.Error(), "absolute http(s) url") {
			t.Errorf("error: got %q, want it to describe the absolute-http(s) shape requirement", err)
		}
	})
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
		slog.Default(),
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

// TestBuildGiteaProviderUsesDiscoveredEndpoints mirrors the OIDC counterpart:
// the Gitea factory is built, instantiated with a callback URL, and the
// resulting goth provider carries the discovered authorization_endpoint (not
// the static fallback). gitea.NewCustomisedURL keeps auth / token / profile
// URLs unexported, so the live observable is the redirect URL returned by
// BeginAuth — its origin / path is the discovered authorization_endpoint.
//
// A regression that reverted buildGiteaProvider to use cfgAuthURL on the
// discovery-populated path would surface here: the BeginAuth URL would carry
// the static config origin instead of the discovery-document origin.
func TestBuildGiteaProviderUsesDiscoveredEndpoints(t *testing.T) {
	const (
		discoveredAuth     = "https://gitea.example.com/login/oauth/authorize"
		discoveredToken    = "https://gitea.example.com/login/oauth/access_token"
		discoveredUserInfo = "https://gitea.example.com/login/oauth/userinfo"
		staticAuth         = "https://static.example.com/login/oauth/authorize"
		staticToken        = "https://static.example.com/login/oauth/access_token"
		staticUserInfo     = "https://static.example.com/login/oauth/userinfo"
	)

	t.Run("discovered authorization_endpoint wins over static config", func(t *testing.T) {
		discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, err := w.Write([]byte(`{
				"issuer":"https://gitea.example.com",
				"authorization_endpoint":"` + discoveredAuth + `",
				"token_endpoint":"` + discoveredToken + `",
				"userinfo_endpoint":"` + discoveredUserInfo + `"
			}`))
			if err != nil {
				t.Errorf("write discovery response: %v", err)
			}
		}))
		t.Cleanup(discoveryServer.Close)

		factory, _, _, err := buildGiteaProvider(
			context.Background(),
			slog.Default(),
			newOIDCDiscoveryHTTPClient(),
			config.GiteaProvider{
				OAuth2Provider: config.OAuth2Provider{Key: "key", Secret: "secret"},
				DiscoveryURL:   discoveryServer.URL,
				AuthURL:        staticAuth,
				TokenURL:       staticToken,
				ProfileURL:     staticUserInfo,
				Label:          "Gitea",
			},
		)
		if err != nil {
			t.Fatalf("build provider: %v", err)
		}

		instance, err := factory("https://xolo.example.com/callback")
		if err != nil {
			t.Fatalf("instantiate gitea provider: %v", err)
		}

		session, err := instance.BeginAuth("test-state")
		if err != nil {
			t.Fatalf("BeginAuth: %v", err)
		}
		authURL, err := session.GetAuthURL()
		if err != nil {
			t.Fatalf("GetAuthURL: %v", err)
		}
		if !strings.HasPrefix(authURL, discoveredAuth) {
			t.Errorf("BeginAuth URL: got %q, want prefix %q (discovered authorization_endpoint must win over static config)", authURL, discoveredAuth)
		}
		if strings.HasPrefix(authURL, staticAuth) {
			t.Errorf("BeginAuth URL: got %q, unexpectedly starts with the static config — buildGiteaProvider would have regressed to cfgAuthURL", authURL)
		}
	})

	t.Run("static authorization_endpoint used when DiscoveryURL is empty", func(t *testing.T) {
		factory, _, _, err := buildGiteaProvider(
			context.Background(),
			slog.Default(),
			newOIDCDiscoveryHTTPClient(),
			config.GiteaProvider{
				OAuth2Provider: config.OAuth2Provider{Key: "key", Secret: "secret"},
				AuthURL:        staticAuth,
				TokenURL:       staticToken,
				ProfileURL:     staticUserInfo,
				Label:          "Gitea",
			},
		)
		if err != nil {
			t.Fatalf("build provider: %v", err)
		}

		instance, err := factory("https://xolo.example.com/callback")
		if err != nil {
			t.Fatalf("instantiate gitea provider: %v", err)
		}

		session, err := instance.BeginAuth("test-state")
		if err != nil {
			t.Fatalf("BeginAuth: %v", err)
		}
		authURL, err := session.GetAuthURL()
		if err != nil {
			t.Fatalf("GetAuthURL: %v", err)
		}
		if !strings.HasPrefix(authURL, staticAuth) {
			t.Errorf("BeginAuth URL: got %q, want prefix %q (static authorization_endpoint must reach the running provider)", authURL, staticAuth)
		}
	})
}
