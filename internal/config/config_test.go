package config

import "testing"

const testSecretKey = "0000000000000000000000000000000000000000000000000000000000000000"

func TestParse_OIDCProvidersList(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)
	t.Setenv("XOLO_HTTP_AUTHN_OIDC_PROVIDERS", "auth0,keycloak")

	t.Setenv("XOLO_HTTP_AUTHN_OIDC_PROVIDER_AUTH0_KEY", "auth0-key")
	t.Setenv("XOLO_HTTP_AUTHN_OIDC_PROVIDER_AUTH0_SECRET", "auth0-secret")
	t.Setenv("XOLO_HTTP_AUTHN_OIDC_PROVIDER_AUTH0_DISCOVERY_URL", "https://tenant.eu.auth0.com/.well-known/openid-configuration")
	t.Setenv("XOLO_HTTP_AUTHN_OIDC_PROVIDER_AUTH0_LABEL", "Auth0")
	t.Setenv("XOLO_HTTP_AUTHN_OIDC_PROVIDER_AUTH0_REQUIRED_AUDIENCE", "https://api.xolo")

	t.Setenv("XOLO_HTTP_AUTHN_OIDC_PROVIDER_KEYCLOAK_KEY", "kc-key")
	t.Setenv("XOLO_HTTP_AUTHN_OIDC_PROVIDER_KEYCLOAK_SECRET", "kc-secret")
	t.Setenv("XOLO_HTTP_AUTHN_OIDC_PROVIDER_KEYCLOAK_DISCOVERY_URL", "https://kc.example.com/realms/x/.well-known/openid-configuration")
	t.Setenv("XOLO_HTTP_AUTHN_OIDC_PROVIDER_KEYCLOAK_LABEL", "Keycloak")

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	got := conf.HTTP.Authn.OIDCProviders
	if len(got) != 2 {
		t.Fatalf("expected 2 OIDC providers, got %d", len(got))
	}

	if got[0].ID != "auth0" || string(got[0].Key) != "auth0-key" || got[0].Label != "Auth0" ||
		string(got[0].DiscoveryURL) != "https://tenant.eu.auth0.com/.well-known/openid-configuration" ||
		got[0].RequiredAudience != "https://api.xolo" {
		t.Errorf("unexpected auth0 provider: %+v", got[0])
	}

	if got[1].ID != "keycloak" || string(got[1].Key) != "kc-key" || got[1].Label != "Keycloak" {
		t.Errorf("unexpected keycloak provider: %+v", got[1])
	}
}

func TestParse_LegacyOIDCSlotBackwardCompat(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)
	// No OIDC_PROVIDERS list; legacy single slot only.
	t.Setenv("XOLO_HTTP_AUTHN_PROVIDERS_OIDC_KEY", "legacy-key")
	t.Setenv("XOLO_HTTP_AUTHN_PROVIDERS_OIDC_SECRET", "legacy-secret")
	t.Setenv("XOLO_HTTP_AUTHN_PROVIDERS_OIDC_DISCOVERY_URL", "https://legacy.example.com/.well-known/openid-configuration")

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	got := conf.HTTP.Authn.OIDCProviders
	if len(got) != 1 {
		t.Fatalf("expected 1 synthesized provider, got %d", len(got))
	}
	if got[0].ID != "openid-connect" || string(got[0].Key) != "legacy-key" {
		t.Errorf("unexpected legacy provider: %+v", got[0])
	}
}

func TestParse_AutoCreateUsers(t *testing.T) {
	t.Run("enabled by default", func(t *testing.T) {
		t.Setenv("XOLO_SECRET_KEY", testSecretKey)

		conf, err := Parse()
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}

		if !conf.HTTP.Authn.AutoCreateUsers {
			t.Error("auto creation of users should be enabled by default")
		}
	})

	t.Run("can be disabled", func(t *testing.T) {
		t.Setenv("XOLO_SECRET_KEY", testSecretKey)
		t.Setenv("XOLO_HTTP_AUTHN_AUTO_CREATE_USERS", "false")

		conf, err := Parse()
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}

		if conf.HTTP.Authn.AutoCreateUsers {
			t.Error("auto creation of users should be disabled")
		}
	})
}

// TestValidate_SecretKey covers the key format: a short but valid hex string
// would silently downgrade AES-256 to a weaker cipher, and a malformed one
// would only fail at the first encryption instead of at start-up.
func TestValidate_SecretKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid 32-byte key", testSecretKey, false},
		{"empty", "", true},
		{"not hex", "zzzz000000000000000000000000000000000000000000000000000000000000", true},
		{"16 bytes", "00000000000000000000000000000000", true},
		{"odd length", "000", true},
		{"64 bytes", testSecretKey + testSecretKey, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSecretKey(tc.key)
			if tc.wantErr && err == nil {
				t.Errorf("key %q: expected an error, got none", tc.key)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("key %q: unexpected error: %v", tc.key, err)
			}
		})
	}
}
