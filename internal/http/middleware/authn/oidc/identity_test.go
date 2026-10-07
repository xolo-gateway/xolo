package oidc

import (
	"testing"

	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

func TestProveIdentity(t *testing.T) {
	h := NewHandler(nil, WithProvidersWithJWKS([]ProviderWithJWKS{
		{ID: "corp", Issuer: "https://id.example.test/", ProvesIssuer: true},
		{ID: "google", Issuer: "https://accounts.google.com", ProvesIssuer: true},
		{ID: "github", Issuer: "https://github.com"},
	}))

	for _, c := range []struct {
		name         string
		provider     string
		raw          map[string]any
		wantIssuer   string
		wantVerified bool
		wantErr      bool
	}{
		{"verified", "corp", map[string]any{"email_verified": true}, "https://id.example.test/", true, false},
		{"verification as a string", "corp", map[string]any{"email_verified": "true"}, "https://id.example.test/", true, false},
		{"unverified", "corp", map[string]any{}, "https://id.example.test/", false, false},
		{"matching iss", "corp", map[string]any{"iss": "https://id.example.test/"}, "https://id.example.test/", false, false},
		{"another iss", "corp", map[string]any{"iss": "https://evil.example.test/"}, "", false, true},
		{"google v2 userinfo", "google", map[string]any{"verified_email": true}, "https://accounts.google.com", true, false},
		{"github proves no issuer", "github", map[string]any{"email_verified": true}, "", true, false},
		{"unknown provider", "other", map[string]any{}, "", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			user := &authn.User{Provider: c.provider, Subject: "u"}
			err := h.proveIdentity(user, c.raw)
			if (err != nil) != c.wantErr {
				t.Fatalf("error: %v", err)
			}
			if c.wantErr {
				return
			}
			if user.Issuer != c.wantIssuer || user.EmailVerified != c.wantVerified {
				t.Errorf("issuer=%q verified=%v, want %q %v", user.Issuer, user.EmailVerified, c.wantIssuer, c.wantVerified)
			}
		})
	}

	if got := h.IdentityIssuers(); len(got) != 2 || got["corp"] != "https://id.example.test/" || got["google"] != "https://accounts.google.com" {
		t.Errorf("identity issuers: %v", got)
	}
}
