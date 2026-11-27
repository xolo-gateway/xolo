package main

import (
	"net/http"
	"strings"
	"testing"
)

// The plugin page loads no input.min.js: a password toggle on it would POST the
// configuration form without toggling anything.
func TestPluginPageHasNoPasswordToggle(t *testing.T) {
	_, ui, _ := newOAuthPlugin(t)
	rec := uiRequest(t, ui, http.MethodGet, "/", "user-1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="auth_value"`) {
		t.Fatalf("auth_value must render for the test to mean anything: %s", body)
	}
	if strings.Contains(body, `data-tui-input-toggle-password`) {
		t.Error("password toggle rendered, but the page loads no input.min.js: clicking would POST the form")
	}
}
