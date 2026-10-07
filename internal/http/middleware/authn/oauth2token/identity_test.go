package oauth2token

import (
	"testing"
)

const testIssuer = "https://id.example.test/"

// The issuer and the email verification travel with the identity; only an
// OIDC subject of the configured issuer proves the issuer.
func TestAuthenticate_IdentityProof(t *testing.T) {
	for _, c := range []struct {
		name         string
		response     map[string]any
		wantIssuer   string
		wantVerified bool
	}{
		{"verified subject", map[string]any{"active": true, "sub": "u", "email": "u@example.test", "email_verified": true}, testIssuer, true},
		{"verification as a string", map[string]any{"active": true, "sub": "u", "email": "u@example.test", "email_verified": "true"}, testIssuer, true},
		{"unverified", map[string]any{"active": true, "sub": "u", "email": "u@example.test", "email_verified": false}, testIssuer, false},
		{"same issuer", map[string]any{"active": true, "sub": "u", "iss": testIssuer}, testIssuer, false},
		{"another issuer", map[string]any{"active": true, "sub": "u", "iss": "https://other.example.test/"}, "", false},
		{"username only", map[string]any{"active": true, "username": "u"}, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := newIntrospectionServer(t, c.response)
			h := NewHandler([]Provider{{ID: "corp", Issuer: testIssuer, IntrospectionURL: srv.URL, ClientID: "xolo"}})
			user, err := h.Authenticate(nil, requestWithToken("good"))
			if err != nil || user == nil {
				t.Fatalf("user=%v err=%v", user, err)
			}
			if user.Issuer != c.wantIssuer || user.EmailVerified != c.wantVerified {
				t.Errorf("issuer=%q verified=%v, want %q %v", user.Issuer, user.EmailVerified, c.wantIssuer, c.wantVerified)
			}
		})
	}

	t.Run("userinfo", func(t *testing.T) {
		srv, _ := newUserInfoServer(t, map[string]any{"sub": "u", "email": "u@example.test", "email_verified": true})
		h := NewHandler([]Provider{{ID: "corp", Issuer: testIssuer, UserInfoURL: srv.URL}})
		user, err := h.Authenticate(nil, requestWithToken("good"))
		if err != nil || user == nil {
			t.Fatalf("user=%v err=%v", user, err)
		}
		if user.Issuer != testIssuer || !user.EmailVerified {
			t.Errorf("unexpected user: %+v", user)
		}
	})

	t.Run("a provider without issuer proves none", func(t *testing.T) {
		srv, _ := newIntrospectionServer(t, map[string]any{"active": true, "sub": "u"})
		h := NewHandler([]Provider{{ID: "corp", IntrospectionURL: srv.URL, ClientID: "xolo"}})
		user, err := h.Authenticate(nil, requestWithToken("good"))
		if err != nil || user == nil || user.Issuer != "" {
			t.Fatalf("user=%+v err=%v", user, err)
		}
	})
}
