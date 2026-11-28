package config

import (
	"fmt"
	"time"
)

// Usage configures what is kept of the requests beside their usage records.
type Usage struct {
	// TracesRetention is how long the trace of a request is kept — the spans
	// of its upstream attempts and pipeline nodes, which the request details
	// show — and the record of a request the provider failed. The usage
	// records the costs are read from are not affected. "0" keeps them
	// forever.
	TracesRetention time.Duration `env:"TRACES_RETENTION,expand" envDefault:"720h"`
}

func (c Usage) Validate() error {
	if c.TracesRetention < 0 {
		return fmt.Errorf("XOLO_USAGE_TRACES_RETENTION must not be negative")
	}
	if c.TracesRetention > 0 && c.TracesRetention < time.Hour {
		return fmt.Errorf("XOLO_USAGE_TRACES_RETENTION must be at least 1h, or 0 to keep the traces forever")
	}
	return nil
}
