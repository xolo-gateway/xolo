package setup

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

const usageTraceRetentionInterval = time.Hour

// startUsageTraceRetentionFromConfig starts the retention of the request
// traces until ctx is done, unless they are kept forever.
var startUsageTraceRetentionFromConfig = createFromConfigOnce(func(ctx context.Context, conf *config.Config) (struct{}, error) {
	retention := conf.Usage.TracesRetention
	if retention == 0 {
		return struct{}{}, nil
	}
	store, err := getUsageStoreFromConfig(ctx, conf)
	if err != nil {
		return struct{}{}, errors.WithStack(err)
	}
	go service.RunUsageTraceRetention(ctx, store, retention, usageTraceRetentionInterval)
	return struct{}{}, nil
})
