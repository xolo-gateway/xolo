package config

import (
	"fmt"
	"time"
)

// PluginsConfig controls where Xolo looks for plugin binaries.
type PluginsConfig struct {
	// Dir is the directory scanned at startup for executable plugin binaries.
	// Set via XOLO_PLUGINS_DIR. An empty or missing directory is not an error.
	Dir string `env:"DIR" envDefault:"./plugins"`

	// MemLimit sets the GOMEMLIMIT environment variable for each plugin subprocess,
	// making the Go GC more aggressive before reaching the limit.
	// Set via XOLO_PLUGINS_MEM_LIMIT. Accepts Go-style suffixes: "512MiB", "1GiB",
	// or raw bytes. Empty string disables the limit (default).
	MemLimit string `env:"MEM_LIMIT" envDefault:""`

	// MaxMessageSize bounds, in bytes, every gRPC message exchanged with a
	// plugin subprocess, in both directions. A PreRequest carries the whole
	// request body, so a long agent conversation can exceed it: the request
	// then fails with a ResourceExhausted error. Set via
	// XOLO_PLUGINS_MAX_MESSAGE_SIZE. Default: 64 MiB. Must lie between
	// MinPluginMaxMessageSize and MaxPluginMaxMessageSize.
	MaxMessageSize int `env:"MAX_MESSAGE_SIZE" envDefault:"67108864"`

	// RestartCooldown is the minimum delay between two consecutive restarts of the
	// same plugin subprocess. Prevents OOM restart storms when the plugin crashes
	// repeatedly. Set via XOLO_PLUGINS_RESTART_COOLDOWN (e.g. "30s", "1m").
	// Set to "0s" to disable. Default: 30s.
	RestartCooldown time.Duration `env:"RESTART_COOLDOWN" envDefault:"30s"`
}

// Bounds of PluginsConfig.MaxMessageSize: below 1 MiB an ordinary
// conversation fails, and protobuf refuses messages of 2 GiB or more.
const (
	MinPluginMaxMessageSize = 1 << 20
	MaxPluginMaxMessageSize = 1 << 30
)

func (c PluginsConfig) Validate() error {
	if c.MaxMessageSize < MinPluginMaxMessageSize || c.MaxMessageSize > MaxPluginMaxMessageSize {
		return fmt.Errorf("XOLO_PLUGINS_MAX_MESSAGE_SIZE must be between %d (1 MiB) and %d (1 GiB) bytes, got %d", MinPluginMaxMessageSize, MaxPluginMaxMessageSize, c.MaxMessageSize)
	}
	return nil
}
