package config

import (
	"testing"
	"time"
)

func TestParse_UsageTracesRetentionDefault(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if conf.Usage.TracesRetention != 720*time.Hour {
		t.Errorf("TracesRetention = %s, want 720h", conf.Usage.TracesRetention)
	}
}

func TestParse_UsageTracesRetention(t *testing.T) {
	for value, valid := range map[string]bool{"0": true, "1h": true, "2160h": true, "30m": false, "-1h": false} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("XOLO_SECRET_KEY", testSecretKey)
			t.Setenv("XOLO_USAGE_TRACES_RETENTION", value)
			if _, err := Parse(); (err == nil) != valid {
				t.Errorf("%s: got %v", value, err)
			}
		})
	}
}
