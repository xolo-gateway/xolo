package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// RunSessionSweep removes, at every interval until ctx is done, the expired
// OIDC sessions, the replay keys of logout tokens that can no longer be
// accepted and the identities left unused. Failures are logged and retried at
// the next tick: the sweep never stops the server. Every replica may run it.
func RunSessionSweep(ctx context.Context, registry port.SessionRegistry, sessionTTL, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := registry.SweepSessions(ctx, time.Now(), sessionTTL); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "oidc session sweep failed", slog.Any("error", errors.WithStack(err)))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
