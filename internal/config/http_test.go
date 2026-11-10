package config

import (
	"testing"
	"time"
)

// A session cookie, and the OIDC session it designates, last 24 hours unless
// configured otherwise.
func TestParse_SessionCookieMaxAge(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if got := conf.HTTP.Session.Cookie.MaxAge; got != 24*time.Hour {
		t.Errorf("default max age: got %v, want 24h", got)
	}

	t.Setenv("XOLO_HTTP_SESSION_COOKIE_MAX_AGE", "8h")
	conf, err = Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if got := conf.HTTP.Session.Cookie.MaxAge; got != 8*time.Hour {
		t.Errorf("max age: got %v, want 8h", got)
	}
}

func TestTrustedProxyPrefixes(t *testing.T) {
	limit := HTTPRateLimit{TrustedProxies: []string{"10.244.0.0/16", " 192.0.2.10 ", "", "2001:db8::/32", "10.1.2.3/8", "::ffff:10.96.0.0/108"}}
	prefixes, err := limit.TrustedProxyPrefixes()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"10.244.0.0/16", "192.0.2.10/32", "2001:db8::/32", "10.0.0.0/8", "10.96.0.0/12"}
	if len(prefixes) != len(want) {
		t.Fatalf("got %v", prefixes)
	}
	for i, prefix := range prefixes {
		if prefix.String() != want[i] {
			t.Fatalf("prefix %d: got %s, want %s", i, prefix, want[i])
		}
	}

	if _, err := (HTTPRateLimit{TrustedProxies: []string{"ingress"}}).TrustedProxyPrefixes(); err == nil {
		t.Fatal("expected an error for a value that is no address")
	}
}
