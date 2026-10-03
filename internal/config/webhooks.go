package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

type Webhooks struct {
	Enabled              bool          `env:"ENABLED" envDefault:"false"`
	AllowedOrigins       []string      `env:"ALLOWED_ORIGINS" envSeparator:","`
	AllowPrivateNetworks bool          `env:"ALLOW_PRIVATE_NETWORKS" envDefault:"false"`
	TLSCAFile            string        `env:"TLS_CA_FILE,expand"`
	Workers              int           `env:"WORKERS" envDefault:"2"`
	PollInterval         time.Duration `env:"POLL_INTERVAL" envDefault:"1s"`
	QueueCapacity        int           `env:"QUEUE_CAPACITY" envDefault:"10000"`
}

func (c Webhooks) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.AllowedOrigins) == 0 {
		return fmt.Errorf("XOLO_WEBHOOKS_ALLOWED_ORIGINS is required")
	}
	for _, raw := range c.AllowedOrigins {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(u.Hostname(), "*") || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("XOLO_WEBHOOKS_ALLOWED_ORIGINS requires HTTPS origins")
		}
	}
	if c.Workers < 1 || c.Workers > 16 || c.QueueCapacity < 1 || c.QueueCapacity > 1000000 || c.PollInterval < 100*time.Millisecond || c.PollInterval > time.Minute {
		return fmt.Errorf("invalid XOLO_WEBHOOKS worker, queue or polling bounds")
	}
	return nil
}
