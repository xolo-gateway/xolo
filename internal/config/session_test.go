package config

import (
	"strings"
	"testing"
)

// A cookie that never expires would outlive its registered OIDC session,
// silently signing users out: such a lifetime is refused at startup.
func TestParse_SessionCookieMaxAgeMustBePositive(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)

	for _, value := range []string{"0", "-1h"} {
		t.Setenv("XOLO_HTTP_SESSION_COOKIE_MAX_AGE", value)
		_, err := Parse()
		if err == nil || !strings.Contains(err.Error(), "XOLO_HTTP_SESSION_COOKIE_MAX_AGE") {
			t.Errorf("max age %s: got %v, want a refusal", value, err)
		}
	}
}
