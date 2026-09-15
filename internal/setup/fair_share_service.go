package setup

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/pkg/errors"
)

// One instance for the whole process: the enforcer decides against it and the
// dashboard displays from it. Two instances would each carry their own
// active-user cache and could, for the length of a TTL, show a gauge the
// enforcer does not apply — the very gap the shared service exists to close.
var getFairShareServiceFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (*service.FairShareService, error) {
	usageStore, err := getUsageStoreFromConfig(ctx, conf)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return service.NewFairShareServiceWithCacheTTL(usageStore, conf.Proxy.ActiveUserCacheTTL), nil
})
