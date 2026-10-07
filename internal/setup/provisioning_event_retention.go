package setup

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
)

const provisioningEventRetentionInterval = time.Hour

// startProvisioningEventRetentionFromConfig starts the retention of the
// provisioning event feed until ctx is done. Local changes are published even
// when the Provisionning API is disabled, so retention always runs, unless it
// is unlimited.
var startProvisioningEventRetentionFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (struct{}, error) {
	retention := conf.ProvisionningAPI.EventRetention
	if retention == 0 {
		return struct{}{}, nil
	}
	provisioning, err := getProvisioningServiceFromConfig(ctx, conf)
	if err != nil {
		return struct{}{}, errors.WithStack(err)
	}
	go provisioning.RunEventRetention(ctx, retention, provisioningEventRetentionInterval)
	return struct{}{}, nil
})
