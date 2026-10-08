package model

import (
	"slices"
	"strings"
	"time"
)

// WebhookID identifies a subscription. It is chosen by the client and unique
// across the instance: it never moves to another tenant.
type WebhookID string

func ParseWebhookID(s string) (WebhookID, error) { v, e := parseUUID(s); return WebhookID(v), e }

// Subscription states. A disabled subscription keeps its state: enabled is a
// separate switch.
const (
	WebhookReady        = "ready"
	WebhookBackpressure = "backpressure"
	WebhookHistoryLost  = "history_lost"
)

// Delivery states.
const (
	WebhookPending   = "pending"
	WebhookLeased    = "leased"
	WebhookDelivered = "delivered"
	WebhookFailed    = "failed"
)

const (
	// WebhookLease bounds one attempt: an expired lease is claimed again.
	WebhookLease = 30 * time.Second
	// WebhookMaxAge bounds the retries of one delivery.
	WebhookMaxAge      = 24 * time.Hour
	WebhookMaxAttempts = 12
	// WebhookRetention keeps finished deliveries for diagnostics.
	WebhookRetention                 = 7 * 24 * time.Hour
	WebhookMaxSubscriptionsPerTenant = 10
)

// WebhookSubscription is the operational view of a subscription. It never
// carries its secrets.
type WebhookSubscription struct {
	ID          WebhookID
	TenantID    TenantID
	Destination string
	Events      []string
	Enabled     bool
	State       string
	Position    int64
	SecretCount int
	UpdatedAt   time.Time
}

// WebhookSettings is a validated subscription write. Empty EncryptedSecrets
// keeps the stored secrets.
type WebhookSettings struct {
	Destination      string
	Events           []string
	Enabled          bool
	EncryptedSecrets string
	SecretCount      int
}

// WebhookJob is one leased attempt. It is internal and must never be
// serialized into diagnostics.
type WebhookJob struct {
	ID, EventID, Body, Destination, EncryptedSecrets, Lease string
	SubscriptionID                                          WebhookID
	TenantID                                                TenantID
	Attempts                                                int
	CreatedAt                                               time.Time
}

type WebhookResult struct {
	Success    bool
	Diagnostic string
	StatusCode int
}

// WebhookCapacity bounds the deliveries waiting or in flight, for the whole
// instance and for each subscription. Finished deliveries never count.
type WebhookCapacity struct {
	Queue, Subscription int
}

type WebhookStats struct {
	Pending, InFlight, Failed, Delivered int64
	HistoryLost, Backpressure            int64
	MaterializationLag, DeliveryLag      time.Duration
}

// WebhookDeliveryStatus excludes payload and credentials.
type WebhookDeliveryStatus struct {
	ID          string
	EventID     string
	Sequence    int64
	State       string
	Attempts    int
	NextAttempt time.Time
	CreatedAt   time.Time
	FinishedAt  *time.Time
	Diagnostic  string
	StatusCode  int
}

// ValidWebhookEvent accepts "*" or the type of an event of the feed.
func ValidWebhookEvent(typ string) bool {
	if typ == "*" {
		return true
	}
	rest, ok := strings.CutSuffix(typ, ".v1")
	if !ok {
		return false
	}
	family, change, ok := strings.Cut(rest, ".")
	if !ok {
		return false
	}
	if !slices.Contains(CommonFamilies, family) && !IsBusinessFamily(family) {
		return false
	}
	switch change {
	case CommonEventCreated, CommonEventUpdated, CommonEventDeleted:
		return true
	}
	return false
}
