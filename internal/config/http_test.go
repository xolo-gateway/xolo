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
