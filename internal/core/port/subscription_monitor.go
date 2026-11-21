package port

import (
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// SubscriptionMonitor gives read-only access to the in-memory subscription
// enforcement state (concurrency counters + exhaustion cooldowns).
// Implemented by proxy.SubscriptionStateDetailed without code change.
type SubscriptionMonitor interface {
	CurrentInFlight(orgID model.OrgID, providerID model.ProviderID) int
	CurrentUserInFlight(orgID model.OrgID, providerID model.ProviderID, userID model.UserID) int
	// IsExhausted reports whether a constraint is in cooldown after an upstream
	// exhaustion on every one of the given API keys of the provider (see
	// model.ProviderKeyIDs). upstreamModel names the model of a per-model
	// constraint and is empty for a provider-wide one.
	IsExhausted(orgID model.OrgID, providerID model.ProviderID, keys []model.ProviderKeyID, label, upstreamModel string) bool
	// ExhaustedUntil is when the cooldown of IsExhausted ends on the first of
	// the given keys to come out of it, zero when one of them is not in
	// cooldown.
	ExhaustedUntil(orgID model.OrgID, providerID model.ProviderID, keys []model.ProviderKeyID, label, upstreamModel string) time.Time
}
