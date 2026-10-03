package config

import (
	"fmt"
	"time"
)

type Lifecycle struct {
	Enabled      bool          `env:"ENABLED" envDefault:"false"`
	Retention    time.Duration `env:"RETENTION" envDefault:"720h"`
	PollInterval time.Duration `env:"POLL_INTERVAL" envDefault:"1m"`
}

func (c Lifecycle) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Retention < time.Second || c.Retention > 3650*24*time.Hour {
		return fmt.Errorf("XOLO_LIFECYCLE_RETENTION must be between 1s and 3650 days")
	}
	if c.PollInterval < time.Second || c.PollInterval > time.Hour {
		return fmt.Errorf("XOLO_LIFECYCLE_POLL_INTERVAL must be between 1s and 1h")
	}
	return nil
}
