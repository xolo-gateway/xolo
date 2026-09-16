package config_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/config"
)

// secretKeyForTest is a syntactically valid XOLO_SECRET_KEY: the root Validate
// checks it first, so every case below has to get past it.
const secretKeyForTest = "0000000000000000000000000000000000000000000000000000000000000000"

func TestValidateMultitenantBaseURL(t *testing.T) {
	for name, testCase := range map[string]struct {
		baseURL      string
		multitenancy config.Multitenancy
		wantErr      string
	}{
		"relative base url is fine on a single-tenant instance": {
			baseURL:      "/",
			multitenancy: config.Multitenancy{Enabled: false, DefaultTenantSlug: "default"},
		},
		"absolute base url with multi-tenancy": {
			baseURL: "https://xolo.example.com",
			multitenancy: config.Multitenancy{
				Enabled:           true,
				HostPattern:       "{tenant}.xolo.example.com",
				DefaultTenantSlug: "default",
			},
		},
		"relative base url with multi-tenancy": {
			baseURL: "/",
			multitenancy: config.Multitenancy{
				Enabled:           true,
				HostPattern:       "{tenant}.xolo.example.com",
				DefaultTenantSlug: "default",
			},
			wantErr: "XOLO_HTTP_BASE_URL must be absolute",
		},
		"schemeless base url with multi-tenancy": {
			baseURL: "xolo.example.com",
			multitenancy: config.Multitenancy{
				Enabled:           true,
				HostPattern:       "{tenant}.xolo.example.com",
				DefaultTenantSlug: "default",
			},
			wantErr: "XOLO_HTTP_BASE_URL must be absolute",
		},
	} {
		t.Run(name, func(t *testing.T) {
			conf := config.Config{
				SecretKey:    secretKeyForTest,
				HTTP:         config.HTTP{BaseURL: testCase.baseURL},
				Multitenancy: testCase.multitenancy,
			}

			err := conf.Validate()

			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("error: got nil, want one containing %q", testCase.wantErr)
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Errorf("error: got %q, want one containing %q", err, testCase.wantErr)
			}
		})
	}
}

// TestNormalizeBaseURL pins the trimming behavior added for issue #28: a
// stray space picked up from a .env file or a ConfigMap, or a trailing
// slash typed by hand, must not leak into the stored value, otherwise the
// downstream consumers (http.WithBaseURL, newTenantBaseURLResolver,
// oidcCallbackURL) end up building malformed URLs.
func TestNormalizeBaseURL(t *testing.T) {
	for name, testCase := range map[string]struct {
		input string
		want  string
	}{
		"empty stays empty": {
			input: "",
			want:  "",
		},
		"root path becomes empty after trailing slash is trimmed": {
			input: "/",
			want:  "",
		},
		"absolute url is untouched": {
			input: "https://xolo.example.com",
			want:  "https://xolo.example.com",
		},
		"leading space is trimmed": {
			input: " https://xolo.example.com",
			want:  "https://xolo.example.com",
		},
		"trailing space is trimmed": {
			input: "https://xolo.example.com ",
			want:  "https://xolo.example.com",
		},
		"surrounding newline is trimmed": {
			input: "\nhttps://xolo.example.com\n",
			want:  "https://xolo.example.com",
		},
		"trailing slash is trimmed": {
			input: "https://xolo.example.com/",
			want:  "https://xolo.example.com",
		},
		"trailing slash and space are trimmed together": {
			input: "  https://xolo.example.com/  ",
			want:  "https://xolo.example.com",
		},
		"single-tenant relative path with trailing slash": {
			input: "/gateway/",
			want:  "/gateway",
		},
	} {
		t.Run(name, func(t *testing.T) {
			conf := config.Config{
				SecretKey:    secretKeyForTest,
				HTTP:         config.HTTP{BaseURL: testCase.input},
				Multitenancy: config.Multitenancy{DefaultTenantSlug: "default"},
			}

			if err := conf.Validate(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got := conf.HTTP.BaseURL; got != testCase.want {
				t.Errorf("HTTP.BaseURL: got %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestNormalizeBaseURLFixesSingleTenantBypass locks in the original symptom
// from issue #28: in single-tenant mode the absolute-URL check is skipped,
// so an untrimmed base URL would silently propagate to every generated URL
// (OAuth redirect_uri included) and break the login at the IdP. After
// normalization the stored value is the canonical one.
//
// The test is meaningful beyond what TestNormalizeBaseURL covers because it
// asserts the contract that downstream consumers rely on: the value that
// reaches http.WithBaseURL, newTenantBaseURLResolver and oidcCallbackURL
// must be parseable as a URL. The raw input would fail url.Parse (a leading
// space makes it look like a relative path with an embedded scheme); the
// canonical form must not.
func TestNormalizeBaseURLFixesSingleTenantBypass(t *testing.T) {
	const raw = " https://xolo.example.com/ "

	if _, err := url.Parse(raw); err == nil {
		t.Fatalf("control: expected url.Parse(%q) to fail so the fix has something to do; got nil", raw)
	}

	conf := config.Config{
		SecretKey:    secretKeyForTest,
		HTTP:         config.HTTP{BaseURL: raw},
		Multitenancy: config.Multitenancy{DefaultTenantSlug: "default"},
	}

	if err := conf.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := conf.HTTP.BaseURL
	if want := "https://xolo.example.com"; got != want {
		t.Errorf("HTTP.BaseURL: got %q, want %q (the value downstream consumers should see)", got, want)
	}

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse on the canonical value: got error %v, want nil so newTenantBaseURLResolver does not refuse the URL", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		t.Errorf("canonical value %q: got scheme=%q host=%q, want a parseable absolute URL", got, parsed.Scheme, parsed.Host)
	}
}
