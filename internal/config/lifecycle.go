package config

import (
	"fmt"
	"time"
)

// Lifecycle configures the deletion of tenants, organizations and members.
// Disabled, no deletion can be recorded and no database guard is installed;
// the guards of deletions already recorded are kept.
type Lifecycle struct {
	Enabled bool `env:"ENABLED" envDefault:"false"`
	// Retention is how long a deleted resource is kept, frozen, before it may
	// be purged.
	Retention time.Duration `env:"RETENTION" envDefault:"720h"`
}

func (c Lifecycle) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Retention < time.Second || c.Retention > 3650*24*time.Hour {
		return fmt.Errorf("XOLO_LIFECYCLE_RETENTION must be between 1s and 3650 days")
	}
	return nil
}
