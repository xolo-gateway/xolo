package model

import "time"

// WebhookSubscription is the non-secret operational view of an instance-managed
// tenant subscription. Ownership transfer is reserved for the adoption extension.
type WebhookSubscription struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Destination string    `json:"destination"`
	Events      []string  `json:"events"`
	Enabled     bool      `json:"enabled"`
	Owner       string    `json:"owner"`
	State       string    `json:"state"`
	Position    int64     `json:"position"`
	SecretCount int       `json:"secret_count"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type WebhookSettings struct {
	Destination      string
	Events           []string
	Enabled          bool
	EncryptedSecrets string
	SecretCount      int
}

// WebhookJob is internal and must never be serialized into diagnostics.
type WebhookJob struct {
	ID, SubscriptionID, TenantID, EventID, Body, Destination, EncryptedSecrets, Lease string
	Attempts                                                                          int
	CreatedAt                                                                         time.Time
}
type WebhookResult struct {
	Success    bool
	Diagnostic string
	StatusCode int
}
type WebhookStats struct {
	Pending            int64   `json:"pending"`
	InFlight           int64   `json:"in_flight"`
	Failed             int64   `json:"failed"`
	Delivered          int64   `json:"delivered"`
	HistoryLost        int64   `json:"history_lost"`
	MaterializationLag float64 `json:"materialization_lag_seconds"`
	DeliveryLag        float64 `json:"delivery_lag_seconds"`
}

const (
	WebhookLease            = 30 * time.Second
	WebhookMaxAge           = 24 * time.Hour
	WebhookRetention        = 7 * 24 * time.Hour
	WebhookMaxAttempts      = 12
	WebhookMaxSubscriptions = 100
)

// WebhookDeliveryStatus excludes payload and credentials from diagnostics.
type WebhookDeliveryStatus struct {
	ID          string     `json:"id"`
	EventID     string     `json:"event_id"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	NextAttempt time.Time  `json:"next_attempt"`
	CreatedAt   time.Time  `json:"created_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	Diagnostic  string     `json:"diagnostic"`
	StatusCode  int        `json:"status_code"`
}
