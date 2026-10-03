package config

import (
	"testing"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

func TestOwnershipConfiguration(t *testing.T) {
	t.Setenv("XOLO_OWNERSHIP", "member=control_plane,subscription=local")
	cfg, err := env.ParseAsWithOptions[Config](env.Options{Prefix: "XOLO_"})
	require.NoError(t, err)
	require.NoError(t, cfg.Ownership.Validate())
	require.Equal(t, model.OwnerControlPlane, cfg.Ownership.Effective()["member"])
	require.Equal(t, model.OwnerLocal, cfg.Ownership.Effective()["tenant"])
	for _, policy := range []model.OwnershipPolicy{{"unknown": "local"}, {"tenant": "external"}, {"member": ""}} {
		require.Error(t, policy.Validate())
	}
}
