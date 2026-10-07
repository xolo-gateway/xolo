package config

import (
	"strings"
	"testing"
	"time"
)

func TestParse_ProvisionningAPIDisabledByDefault(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if conf.ProvisionningAPI.Enabled {
		t.Error("provisionning api should be disabled by default")
	}
	if conf.ProvisionningAPI.Address != ":3003" {
		t.Errorf("address: got %q, want %q", conf.ProvisionningAPI.Address, ":3003")
	}
	if conf.ProvisionningAPI.ShutdownTimeout != 10*time.Second {
		t.Errorf("shutdown timeout: got %v, want %v", conf.ProvisionningAPI.ShutdownTimeout, 10*time.Second)
	}
	if conf.ProvisionningAPI.EventRetention != 720*time.Hour {
		t.Errorf("event retention: got %v, want %v", conf.ProvisionningAPI.EventRetention, 720*time.Hour)
	}
}

func TestProvisionningEventRetentionValidation(t *testing.T) {
	// Retention applies even with the listener disabled: local changes are
	// published in any case.
	for _, retention := range []time.Duration{0, time.Hour} {
		c := ProvisionningAPI{EventRetention: retention}
		if err := c.Validate(); err != nil {
			t.Errorf("retention %v refused: %v", retention, err)
		}
	}
	c := ProvisionningAPI{EventRetention: -time.Hour}
	if err := c.Validate(); err == nil {
		t.Error("negative retention accepted")
	}
}

func TestParse_ProvisionningAPIEnabled(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)
	t.Setenv("XOLO_PROVISIONNING_API_ENABLED", "true")
	t.Setenv("XOLO_PROVISIONNING_API_ADDRESS", ":4443")
	t.Setenv("XOLO_PROVISIONNING_API_AUTHORIZED_URIS", "urn:xolo:test:console")
	t.Setenv("XOLO_PROVISIONNING_API_TLS_CERT_FILE", "/etc/xolo/provisionning.crt")
	t.Setenv("XOLO_PROVISIONNING_API_TLS_KEY_FILE", "/etc/xolo/provisionning.key")
	t.Setenv("XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE", "/etc/xolo/ca.crt")
	t.Setenv("XOLO_PROVISIONNING_API_SHUTDOWN_TIMEOUT", "30s")

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	provisionningAPI := conf.ProvisionningAPI

	if !provisionningAPI.Enabled {
		t.Error("provisionning api should be enabled")
	}
	if provisionningAPI.Address != ":4443" {
		t.Errorf("address: got %q", provisionningAPI.Address)
	}
	if provisionningAPI.TLSCertFile != "/etc/xolo/provisionning.crt" || provisionningAPI.TLSKeyFile != "/etc/xolo/provisionning.key" || provisionningAPI.TLSClientCAFile != "/etc/xolo/ca.crt" {
		t.Errorf("tls files: got %+v", provisionningAPI)
	}
	if provisionningAPI.ShutdownTimeout != 30*time.Second {
		t.Errorf("shutdown timeout: got %v", provisionningAPI.ShutdownTimeout)
	}
}

// TestParse_ProvisionningAPIEnabledWithoutTLS checks that enabling the Provisionning API
// without the mutual TLS material fails at startup, naming the missing
// variable. There is no anonymous fallback.
func TestParse_ProvisionningAPIEnabledWithoutTLS(t *testing.T) {
	for missing, env := range map[string]map[string]string{
		"XOLO_PROVISIONNING_API_TLS_CERT_FILE": {
			"XOLO_PROVISIONNING_API_TLS_KEY_FILE":       "/etc/xolo/provisionning.key",
			"XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE": "/etc/xolo/ca.crt",
		},
		"XOLO_PROVISIONNING_API_TLS_KEY_FILE": {
			"XOLO_PROVISIONNING_API_TLS_CERT_FILE":      "/etc/xolo/provisionning.crt",
			"XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE": "/etc/xolo/ca.crt",
		},
		"XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE": {
			"XOLO_PROVISIONNING_API_TLS_CERT_FILE": "/etc/xolo/provisionning.crt",
			"XOLO_PROVISIONNING_API_TLS_KEY_FILE":  "/etc/xolo/provisionning.key",
		},
	} {
		t.Run("missing "+missing, func(t *testing.T) {
			t.Setenv("XOLO_SECRET_KEY", testSecretKey)
			t.Setenv("XOLO_PROVISIONNING_API_ENABLED", "true")
			for name, value := range env {
				t.Setenv(name, value)
			}

			_, err := Parse()
			if err == nil {
				t.Fatal("Parse should have failed")
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error should name %s, got %v", missing, err)
			}
		})
	}
}

func TestProvisionningAuthorityValidation(t *testing.T) {
	valid := ProvisionningAPI{Enabled: true, Address: ":3003", TLSCertFile: "cert", TLSKeyFile: "key", TLSClientCAFile: "ca", AuthorizedURIs: []string{"urn:example:console"}, RateLimit: 10, RateBurst: 20}
	for _, uris := range [][]string{nil, {""}, {"relative"}, {" urn:example:console"}, {"urn:example:console", "urn:example:console"}} {
		c := valid
		c.AuthorizedURIs = uris
		if err := c.Validate(); err == nil {
			t.Errorf("invalid URI configuration accepted: %v", uris)
		}
	}
	for _, rate := range []float64{0, -1} {
		c := valid
		c.RateLimit = rate
		if err := c.Validate(); err == nil {
			t.Error("invalid rate accepted")
		}
	}
	c := valid
	c.RateBurst = 0
	if err := c.Validate(); err == nil {
		t.Error("invalid burst accepted")
	}
}
