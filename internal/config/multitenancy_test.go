package config_test

import (
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/config"
)

func TestMultitenancyValidate(t *testing.T) {
	for name, testCase := range map[string]struct {
		conf    config.Multitenancy
		wantErr string
	}{
		"disabled needs no host pattern": {
			conf: config.Multitenancy{Enabled: false, DefaultTenantSlug: "default"},
		},
		"enabled with a valid pattern": {
			conf: config.Multitenancy{Enabled: true, HostPattern: "{tenant}.xolo.example.com", DefaultTenantSlug: "default"},
		},
		"enabled without a host pattern": {
			conf:    config.Multitenancy{Enabled: true, DefaultTenantSlug: "default"},
			wantErr: "XOLO_MULTITENANCY_HOST_PATTERN is required",
		},
		"enabled without the placeholder": {
			conf:    config.Multitenancy{Enabled: true, HostPattern: "xolo.example.com", DefaultTenantSlug: "default"},
			wantErr: "must contain the {tenant} placeholder",
		},
		"placeholder repeated": {
			conf:    config.Multitenancy{Enabled: true, HostPattern: "{tenant}.{tenant}.example.com", DefaultTenantSlug: "default"},
			wantErr: "exactly once",
		},
		"empty default slug": {
			conf:    config.Multitenancy{DefaultTenantSlug: "  "},
			wantErr: "can not be empty",
		},
		"malformed default slug": {
			conf:    config.Multitenancy{DefaultTenantSlug: "Not A Slug"},
			wantErr: "is not a valid slug",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := testCase.conf.Validate()

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
