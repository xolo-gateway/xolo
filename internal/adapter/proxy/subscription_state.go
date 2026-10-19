package proxy

import (
	"sync"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type scopeKey struct {
	orgID      model.OrgID
	providerID model.ProviderID
}

// SubscriptionState holds in-memory per-scope state for subscription enforcement.
// State is not shared across replicas; the upstream resync on 429 acts as a safety net.
type SubscriptionState struct {
	mu          sync.Mutex
	inFlight    map[scopeKey]int
	exhaustedAt map[scopeKey]time.Time // per-constraint label
}

// constraintKey identifies one constraint's cooldown. upstreamModel is set for
// a per-model constraint, whose models are exhausted one by one, and empty for
// a provider-wide one.
type constraintKey struct {
	scope         scopeKey
	label         string
	upstreamModel string
}

type userScopeKey struct {
	orgID      model.OrgID
	providerID model.ProviderID
	userID     model.UserID
}

type SubscriptionStateDetailed struct {
	mu             sync.Mutex
	inFlight       map[scopeKey]int
	userInFlight   map[userScopeKey]int
	exhaustedUntil map[constraintKey]time.Time
	// exhaustedPurgeAt is the size past which MarkExhausted sweeps expired
	// cooldowns.
	exhaustedPurgeAt int
}

func NewSubscriptionState() *SubscriptionStateDetailed {
	return &SubscriptionStateDetailed{
		inFlight:         make(map[scopeKey]int),
		userInFlight:     make(map[userScopeKey]int),
		exhaustedUntil:   make(map[constraintKey]time.Time),
		exhaustedPurgeAt: minPurgeAt,
	}
}

// IncrementInFlight atomically increments the in-flight counter and returns the new value.
func (s *SubscriptionStateDetailed) IncrementInFlight(orgID model.OrgID, providerID model.ProviderID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := scopeKey{orgID, providerID}
	s.inFlight[k]++
	return s.inFlight[k]
}

// DecrementInFlight atomically decrements the in-flight counter (floors at 0).
func (s *SubscriptionStateDetailed) DecrementInFlight(orgID model.OrgID, providerID model.ProviderID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := scopeKey{orgID, providerID}
	if s.inFlight[k] > 0 {
		s.inFlight[k]--
	}
}

// CurrentInFlight returns the current number of in-flight requests for the scope.
func (s *SubscriptionStateDetailed) CurrentInFlight(orgID model.OrgID, providerID model.ProviderID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight[scopeKey{orgID, providerID}]
}

// IncrementUserInFlight atomically increments the per-user in-flight counter and returns the new value.
func (s *SubscriptionStateDetailed) IncrementUserInFlight(orgID model.OrgID, providerID model.ProviderID, userID model.UserID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := userScopeKey{orgID, providerID, userID}
	s.userInFlight[k]++
	return s.userInFlight[k]
}

// DecrementUserInFlight atomically decrements the per-user in-flight counter (floors at 0).
func (s *SubscriptionStateDetailed) DecrementUserInFlight(orgID model.OrgID, providerID model.ProviderID, userID model.UserID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := userScopeKey{orgID, providerID, userID}
	if s.userInFlight[k] > 0 {
		s.userInFlight[k]--
	}
}

// CurrentUserInFlight returns the per-user in-flight count for the given scope.
func (s *SubscriptionStateDetailed) CurrentUserInFlight(orgID model.OrgID, providerID model.ProviderID, userID model.UserID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userInFlight[userScopeKey{orgID, providerID, userID}]
}

// MarkExhausted sets a cooldown for a specific constraint label until the given
// time. upstreamModel names the model of a per-model constraint, empty for a
// provider-wide one.
func (s *SubscriptionStateDetailed) MarkExhausted(orgID model.OrgID, providerID model.ProviderID, label, upstreamModel string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// An expired cooldown is otherwise only dropped when its key is checked
	// again, and per-model cooldowns of models nobody requests any more never
	// are. Sweeping once the map outgrows its last sweep keeps the cost
	// amortised over the insertions.
	if len(s.exhaustedUntil) >= s.exhaustedPurgeAt {
		now := time.Now()
		for k, u := range s.exhaustedUntil {
			if now.After(u) {
				delete(s.exhaustedUntil, k)
			}
		}
		s.exhaustedPurgeAt = max(2*len(s.exhaustedUntil), minPurgeAt)
	}
	s.exhaustedUntil[constraintKey{scopeKey{orgID, providerID}, label, upstreamModel}] = until
}

// IsExhausted reports whether a constraint is currently in cooldown, for one
// upstream model when the constraint is per model.
func (s *SubscriptionStateDetailed) IsExhausted(orgID model.OrgID, providerID model.ProviderID, label, upstreamModel string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := constraintKey{scopeKey{orgID, providerID}, label, upstreamModel}
	until, ok := s.exhaustedUntil[key]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(s.exhaustedUntil, key)
		return false
	}
	return true
}
