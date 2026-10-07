package config

import (
	"net/url"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// Webhooks configures the delivery of the event feed to the subscriptions
// declared through the Provisionning API. It is disabled by default.
type Webhooks struct {
	Enabled bool `env:"ENABLED" envDefault:"false"`
	// AllowedOrigins are the only HTTPS origins a subscription may target.
	AllowedOrigins []string `env:"ALLOWED_ORIGINS" envSeparator:","`
	// AllowPrivateNetworks also allows loopback and private addresses.
	// Link-local addresses, cloud metadata included, stay refused.
	AllowPrivateNetworks bool `env:"ALLOW_PRIVATE_NETWORKS" envDefault:"false"`
	// TLSCAFile adds trusted roots to the system ones.
	TLSCAFile    string        `env:"TLS_CA_FILE,expand"`
	Workers      int           `env:"WORKERS" envDefault:"2"`
	PollInterval time.Duration `env:"POLL_INTERVAL" envDefault:"1s"`
	// QueueCapacity bounds the deliveries waiting or in flight on the
	// instance, SubscriptionCapacity those of one subscription.
	QueueCapacity        int `env:"QUEUE_CAPACITY" envDefault:"10000"`
	SubscriptionCapacity int `env:"SUBSCRIPTION_CAPACITY" envDefault:"1000"`
}

func (c *Webhooks) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.AllowedOrigins) == 0 {
		return errors.New("XOLO_WEBHOOKS_ALLOWED_ORIGINS is required but not set when XOLO_WEBHOOKS_ENABLED is true")
	}
	for _, raw := range c.AllowedOrigins {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
			u.Fragment != "" || strings.ContainsAny(u.Hostname(), "*%") || (u.Path != "" && u.Path != "/") {
			return errors.Errorf("XOLO_WEBHOOKS_ALLOWED_ORIGINS: %q is not an HTTPS origin", raw)
		}
	}
	if c.Workers < 1 || c.Workers > 16 {
		return errors.New("XOLO_WEBHOOKS_WORKERS must be between 1 and 16")
	}
	if c.PollInterval < 100*time.Millisecond || c.PollInterval > time.Minute {
		return errors.New("XOLO_WEBHOOKS_POLL_INTERVAL must be between 100ms and 1m")
	}
	if c.QueueCapacity < 1 || c.QueueCapacity > 1_000_000 {
		return errors.New("XOLO_WEBHOOKS_QUEUE_CAPACITY must be between 1 and 1000000")
	}
	if c.SubscriptionCapacity < 1 || c.SubscriptionCapacity > c.QueueCapacity {
		return errors.New("XOLO_WEBHOOKS_SUBSCRIPTION_CAPACITY must be between 1 and XOLO_WEBHOOKS_QUEUE_CAPACITY")
	}
	return nil
}
