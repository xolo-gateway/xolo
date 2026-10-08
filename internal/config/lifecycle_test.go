package config

import (
	"testing"
	"time"
)

func TestParse_LifecycleDisabledByDefault(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if conf.Lifecycle.Enabled || conf.Lifecycle.Retention != 720*time.Hour {
		t.Errorf("unexpected defaults: %+v", conf.Lifecycle)
	}
}

func TestParse_LifecycleRetention(t *testing.T) {
	for value, valid := range map[string]bool{"24h": true, "1s": true, "0s": false, "87601h": false} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("XOLO_SECRET_KEY", testSecretKey)
			t.Setenv("XOLO_LIFECYCLE_ENABLED", "true")
			t.Setenv("XOLO_LIFECYCLE_RETENTION", value)
			if _, err := Parse(); (err == nil) != valid {
				t.Errorf("%s: got %v", value, err)
			}
		})
	}
}
