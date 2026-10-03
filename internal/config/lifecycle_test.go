package config

import (
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/require"
)

func TestLifecycleConfiguration(t *testing.T) {
	c, err := env.ParseAsWithOptions[Config](env.Options{Prefix: "LOT6_TEST_"})
	require.NoError(t, err)
	require.False(t, c.Lifecycle.Enabled)
	require.Equal(t, 720*time.Hour, c.Lifecycle.Retention)
	require.Equal(t, time.Minute, c.Lifecycle.PollInterval)
	for _, c := range []Lifecycle{{true, 0, time.Minute}, {true, 3651 * 24 * time.Hour, time.Minute}, {true, time.Hour, 0}, {true, time.Hour, 2 * time.Hour}} {
		require.Error(t, c.Validate())
	}
	require.NoError(t, (Lifecycle{true, time.Second, time.Second}).Validate())
}
