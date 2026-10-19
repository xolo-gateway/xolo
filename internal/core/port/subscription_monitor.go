package port

import "github.com/xolo-gateway/xolo/internal/core/model"

// SubscriptionMonitor gives read-only access to the in-memory subscription
// enforcement state (concurrency counters + exhaustion cooldowns).
// Implemented by proxy.SubscriptionStateDetailed without code change.
type SubscriptionMonitor interface {
	CurrentInFlight(orgID model.OrgID, providerID model.ProviderID) int
	CurrentUserInFlight(orgID model.OrgID, providerID model.ProviderID, userID model.UserID) int
	// IsExhausted reports whether a constraint is in cooldown after an upstream
	// exhaustion. upstreamModel names the model of a per-model constraint and
	// is empty for a provider-wide one.
	IsExhausted(orgID model.OrgID, providerID model.ProviderID, label, upstreamModel string) bool
}
