package setup

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

var getTenantStoreFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (port.TenantStore, error) {
	store, err := getGormStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return store, nil
})
