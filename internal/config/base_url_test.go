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
		// Pinned by issue #28: a padded absolute URL (stray space from
		// .env or ConfigMap) must still pass multi-tenant validation
		// after the whitespace and trailing-slash normalization. The
		// accept-side contract is what TestNormalizeBaseURLFixesMultitenantValidationBypass
		// pins in full (stored value + parseability); here we only
		// guard against a regression that would re-introduce the
		// rejection.
		"padded absolute url with multi-tenancy": {
			baseURL: "  https://xolo.example.com/  ",
			multitenancy: config.Multitenancy{
				Enabled:           true,
				HostPattern:       "{tenant}.xolo.example.com",
				DefaultTenantSlug: "default",
			},
		},
		// Pinned by issue #28: a padded schemeless URL with multi-tenancy
		// must still be rejected after the whitespace and trailing-slash
		// normalization — trimming does not introduce a scheme, so the
		// lenient url.Parse keeps Scheme empty and validateMultitenantBaseURL
		// refuses it. The reject-side contract is well covered here; the
		// accept-side contract lives in TestNormalizeBaseURLFixesMultitenantValidationBypass.
		"padded schemeless url with multi-tenancy": {
			baseURL: "  xolo.example.com  ",
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

// TestNormalizeBaseURL pins the trimming behaviour added for issue #28: a
// stray space picked up from a .env file or a ConfigMap, or a trailing
// slash typed by hand, must not leak into the stored value, otherwise the
// downstream consumers (http.WithBaseURL, newTenantBaseURLResolver,
// oidcCallbackURL) end up building malformed URLs.
//
// The joined-path assertion catches the regression reviewed in the PR:
// url.Parse("").JoinPath("/x") returns "x" (no leading slash), which
// browsers then resolve against the current request directory instead of
// the root. The "/" fallback inside normalizeBaseURL keeps every input
// that trims to empty on a parseable root path: the envDefault "/", any
// whitespace-only shape (" ", a lone newline, tabs), and a literal ""
// (which caarlos0/env v11.3.1 can deliver from XOLO_HTTP_BASE_URL='${VAR}'
// with VAR unset — the env tag runs os.Expand after getOr has substituted
// envDefault, so an unset expansion reaches normalizeBaseURL as "",
// not "/").
func TestNormalizeBaseURL(t *testing.T) {
	for name, testCase := range map[string]struct {
		input   string
		want    string
		wantURL string
	}{
		// Literal empty input. Reachable from XOLO_HTTP_BASE_URL='${VAR}'
		// with VAR unset (env lib expands after envDefault substitution),
		// and from any programmatic construction that hasn't been
		// initialised yet. normalizeBaseURL must collapse it to "/" so
		// downstream JoinPath calls keep the leading slash; the
		// "documented limitation" pinned before the fix is gone.
		"empty falls back to slash": {
			input:   "",
			want:    "/",
			wantURL: "/x",
		},
		"root path collapses to slash so url.Parse keeps a path": {
			input:   "/",
			want:    "/",
			wantURL: "/x",
		},
		"single space falls back to slash (issue #28 protection)": {
			input:   " ",
			want:    "/",
			wantURL: "/x",
		},
		"single newline falls back to slash": {
			input:   "\n",
			want:    "/",
			wantURL: "/x",
		},
		"whitespace-only with tabs falls back to slash": {
			input:   " \t\n ",
			want:    "/",
			wantURL: "/x",
		},
		"absolute url is untouched": {
			input:   "https://xolo.example.com",
			want:    "https://xolo.example.com",
			wantURL: "https://xolo.example.com/x",
		},
		"leading space is trimmed": {
			input:   " https://xolo.example.com",
			want:    "https://xolo.example.com",
			wantURL: "https://xolo.example.com/x",
		},
		"trailing space is trimmed": {
			input:   "https://xolo.example.com ",
			want:    "https://xolo.example.com",
			wantURL: "https://xolo.example.com/x",
		},
		"surrounding newline is trimmed": {
			input:   "\nhttps://xolo.example.com\n",
			want:    "https://xolo.example.com",
			wantURL: "https://xolo.example.com/x",
		},
		"trailing slash is trimmed": {
			input:   "https://xolo.example.com/",
			want:    "https://xolo.example.com",
			wantURL: "https://xolo.example.com/x",
		},
		"trailing slash and space are trimmed together": {
			input:   "  https://xolo.example.com/  ",
			want:    "https://xolo.example.com",
			wantURL: "https://xolo.example.com/x",
		},
		"single-tenant relative path with trailing slash": {
			input:   "/gateway/",
			want:    "/gateway",
			wantURL: "/gateway/x",
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

			// The contract the downstream consumers rely on: url.Parse
			// on the stored value, joined with a path, must produce a
			// URL the browser can resolve against the root, not the
			// current request directory. Without this assertion a
			// pin of "/" -> "" as the stored value (i.e. the
			// whitespace-only path the literal-empty branch produces
			// before the fix) would have looked fine in isolation
			// while the consumers handed "x" to the browser.
			parsed, err := url.Parse(conf.HTTP.BaseURL)
			if err != nil {
				t.Fatalf("url.Parse on the stored value %q: got error %v, want nil", conf.HTTP.BaseURL, err)
			}
			if got := parsed.JoinPath("/x").String(); got != testCase.wantURL {
				t.Errorf("url.Parse(conf.HTTP.BaseURL).JoinPath(\"/x\").String(): got %q, want %q", got, testCase.wantURL)
			}
		})
	}
}

// TestNormalizeBaseURLFixesMultitenantValidationBypass locks in the
// original symptom from issue #28: a stray space picked up from a .env
// file or a ConfigMap used to clear Validate() and then fail further
// down in newTenantBaseURLResolver.
//
// Concretely, url.Parse on a padded absolute URL is strict: the leading
// space turns the URL into an opaque relative reference and net/url
// rejects it with "first path segment in URL cannot contain colon",
// returning nil. newTenantBaseURLResolver wrapped that into
// "could not parse base url" (the symptom described in the comment at
// the top of Validate). A schemeless input like xolo.example.com parses
// without error but with empty Scheme/Host, and trips the
// "must be absolute (scheme and host) when multi-tenancy is enabled"
// branch instead.
//
// normalizeBaseURL replaces the padded input with its trimmed, slash-less
// canonical form before either consumer sees it, so url.Parse accepts it
// and the absolute-URL check passes.
//
// Run with Multitenancy.Enabled = true so validateMultitenantBaseURL is
// actually exercised; with multi-tenancy disabled the absolute-URL check
// is skipped entirely and the test would degenerate into a stored-string
// assertion that the lighter TestNormalizeBaseURL already covers.
//
// The test is meaningful beyond what TestNormalizeBaseURL covers because
// it asserts the contract that downstream consumers rely on: the value
// that reaches http.WithBaseURL, newTenantBaseURLResolver and
// oidcCallbackURL must be parseable as an absolute URL, and JoinPath on
// it must produce a URL the browser can resolve against the root (not
// the current request directory, which is what
// url.Parse("").JoinPath(...) silently regresses to). The raw input
// fails both clauses; the canonical form satisfies them.
func TestNormalizeBaseURLFixesMultitenantValidationBypass(t *testing.T) {
	const raw = " https://xolo.example.com/ "

	conf := config.Config{
		SecretKey: secretKeyForTest,
		HTTP:      config.HTTP{BaseURL: raw},
		Multitenancy: config.Multitenancy{
			Enabled:           true,
			HostPattern:       "{tenant}.xolo.example.com",
			DefaultTenantSlug: "default",
		},
	}

	if err := conf.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := conf.HTTP.BaseURL
	if want := "https://xolo.example.com"; got != want {
		t.Errorf("HTTP.BaseURL: got %q, want %q (the value downstream consumers should see)", got, want)
	}

	// The real contract: the value that flows into http.WithBaseURL,
	// newTenantBaseURLResolver and oidcCallbackURL must be parseable as an
	// absolute URL. This is the assertion that catches a regression of
	// normalizeBaseURL or of the call site in Validate: without the fix, the
	// raw input would reach here unchanged and url.Parse would either reject
	// it or return empty Scheme/Host, tripping the "must be absolute (scheme
	// and host) when multi-tenancy is enabled" branch downstream.
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("url.Parse on the stored value %q: got error %v, want nil so newTenantBaseURLResolver does not refuse the URL", got, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		t.Errorf("stored value %q: got scheme=%q host=%q, want a parseable absolute URL", got, parsed.Scheme, parsed.Host)
	}

	// The contract the downstream consumers rely on: url.Parse on the
	// stored value, joined with a path, must produce an absolute URL the
	// browser can resolve against the root, not the current request
	// directory.
	if joined := parsed.JoinPath("/x").String(); joined != "https://xolo.example.com/x" {
		t.Errorf("url.Parse(conf.HTTP.BaseURL).JoinPath(\"/x\").String(): got %q, want %q", joined, "https://xolo.example.com/x")
	}
}
