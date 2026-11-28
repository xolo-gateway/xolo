package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// RunUsageTraceRetention purges the request traces and the request failures
// older than retention at every interval, until ctx is done. Failures are
// logged and retried at the next tick: retention never stops the server.
// Every instance sharing the database may run it; each pass deletes what is
// due, so concurrent passes only find less to delete.
func RunUsageTraceRetention(ctx context.Context, store port.UsageStore, retention, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		before := time.Now().Add(-retention)
		purge := func(what string, run func(context.Context, time.Time) (int64, error)) {
			purged, err := run(ctx, before)
			switch {
			case err != nil && ctx.Err() == nil:
				slog.ErrorContext(ctx, what+" retention failed", slog.Any("error", errors.WithStack(err)))
			case purged > 0:
				slog.InfoContext(ctx, what+" purged", slog.Int64("count", purged), slog.Duration("retention", retention))
			}
		}
		purge("usage traces", store.PurgeUsageTraces)
		purge("request failures", store.PurgeRequestFailures)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
