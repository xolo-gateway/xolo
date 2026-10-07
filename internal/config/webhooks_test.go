package config

import (
	"testing"
	"time"
)

func TestParse_WebhooksDisabledByDefault(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	w := conf.Webhooks
	if w.Enabled || w.Workers != 2 || w.PollInterval != time.Second || w.QueueCapacity != 10000 || w.SubscriptionCapacity != 1000 {
		t.Errorf("unexpected defaults: %+v", w)
	}
}

func TestParse_WebhooksEnabled(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)
	t.Setenv("XOLO_WEBHOOKS_ENABLED", "true")
	t.Setenv("XOLO_WEBHOOKS_ALLOWED_ORIGINS", "https://hooks.example.test,https://other.example.test:8443")

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(conf.Webhooks.AllowedOrigins) != 2 {
		t.Errorf("origins: got %v", conf.Webhooks.AllowedOrigins)
	}
}

func TestWebhooksValidation(t *testing.T) {
	valid := func() Webhooks {
		return Webhooks{Enabled: true, AllowedOrigins: []string{"https://hooks.example.test"}, Workers: 2, PollInterval: time.Second, QueueCapacity: 100, SubscriptionCapacity: 10}
	}
	if err := (&Webhooks{}).Validate(); err != nil {
		t.Errorf("disabled configuration refused: %v", err)
	}
	c := valid()
	if err := c.Validate(); err != nil {
		t.Errorf("valid configuration refused: %v", err)
	}
	for name, mutate := range map[string]func(*Webhooks){
		"no origins":           func(c *Webhooks) { c.AllowedOrigins = nil },
		"http origin":          func(c *Webhooks) { c.AllowedOrigins = []string{"http://hooks.example.test"} },
		"origin with path":     func(c *Webhooks) { c.AllowedOrigins = []string{"https://hooks.example.test/x"} },
		"origin with userinfo": func(c *Webhooks) { c.AllowedOrigins = []string{"https://u@hooks.example.test"} },
		"wildcard origin":      func(c *Webhooks) { c.AllowedOrigins = []string{"https://*.example.test"} },
		"no workers":           func(c *Webhooks) { c.Workers = 0 },
		"too many workers":     func(c *Webhooks) { c.Workers = 17 },
		"poll too fast":        func(c *Webhooks) { c.PollInterval = 0 },
		"poll too slow":        func(c *Webhooks) { c.PollInterval = 2 * time.Minute },
		"no queue":             func(c *Webhooks) { c.QueueCapacity = 0 },
		"no subscription room": func(c *Webhooks) { c.SubscriptionCapacity = 0 },
		"subscription > queue": func(c *Webhooks) { c.SubscriptionCapacity = 101 },
	} {
		c := valid()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
