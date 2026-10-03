package config

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestWebhooksConfig(t *testing.T) {
	require.NoError(t, (Webhooks{}).Validate())
	base := Webhooks{Enabled: true, AllowedOrigins: []string{"https://console.example.test"}, Workers: 2, PollInterval: time.Second, QueueCapacity: 10000}
	require.NoError(t, base.Validate())
	for _, change := range []func(*Webhooks){func(c *Webhooks) { c.AllowedOrigins = nil }, func(c *Webhooks) { c.AllowedOrigins = []string{"http://console.test"} }, func(c *Webhooks) { c.AllowedOrigins = []string{"https://console.test/path"} }, func(c *Webhooks) { c.Workers = 0 }, func(c *Webhooks) { c.Workers = 17 }, func(c *Webhooks) { c.PollInterval = 0 }, func(c *Webhooks) { c.QueueCapacity = 0 }} {
		c := base
		change(&c)
		require.Error(t, c.Validate())
	}
}
