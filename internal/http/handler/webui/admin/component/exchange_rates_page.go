package component

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// lastSyncMessage states when the cache was last filled, from the freshest rate
// it holds — the service does not record a synchronisation timestamp of its own.
func lastSyncMessage(ctx context.Context, rates []model.ExchangeRate) string {
	if len(rates) == 0 {
		return i18n.T(ctx, "admin.exchange_rates.never_synced")
	}

	latest := rates[0].FetchedAt
	for _, rate := range rates[1:] {
		if rate.FetchedAt.After(latest) {
			latest = rate.FetchedAt
		}
	}

	return i18n.T(ctx, "admin.exchange_rates.last_sync",
		latest.Format("02/01/2006"), latest.Format("15:04"),
	)
}
