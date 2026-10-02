package config

import (
	"testing"
	"time"
)

func TestParse_SessionCookieMaxAgeDefault(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if got, want := conf.HTTP.Session.Cookie.MaxAge, 24*time.Hour; got != want {
		t.Errorf("session cookie MaxAge default: got %v, want %v", got, want)
	}
}

func TestParse_SessionCookieMaxAgeOverride(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)
	t.Setenv("XOLO_HTTP_SESSION_COOKIE_MAX_AGE", "1h")

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if got, want := conf.HTTP.Session.Cookie.MaxAge, time.Hour; got != want {
		t.Errorf("session cookie MaxAge override: got %v, want %v", got, want)
	}
}
