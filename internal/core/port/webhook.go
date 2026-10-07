package port

import (
	"context"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// WebhookStore persists subscriptions and their delivery queue. None of its
// operations takes the publication lock of the event feed.
type WebhookStore interface {
	// PutWebhook creates or updates a subscription of tenant. A new one starts
	// at the end of the feed.
	PutWebhook(ctx context.Context, tenant model.TenantID, id model.WebhookID, settings model.WebhookSettings) (model.WebhookSubscription, error)
	GetWebhook(ctx context.Context, tenant model.TenantID, id model.WebhookID) (model.WebhookSubscription, error)
	ListWebhooks(ctx context.Context, tenant model.TenantID) ([]model.WebhookSubscription, error)
	// DeleteWebhook removes the subscription and its deliveries.
	DeleteWebhook(ctx context.Context, tenant model.TenantID, id model.WebhookID) error
	// ResetWebhook drops the pending deliveries and moves the subscription to
	// the end of the feed.
	ResetWebhook(ctx context.Context, tenant model.TenantID, id model.WebhookID) error
	// ListWebhookDeliveries returns the latest deliveries of a subscription.
	ListWebhookDeliveries(ctx context.Context, tenant model.TenantID, id model.WebhookID) ([]model.WebhookDeliveryStatus, error)

	// PrepareWebhooks turns the feed events into deliveries, at most budget
	// events per subscription.
	PrepareWebhooks(ctx context.Context, capacity model.WebhookCapacity, budget int) error
	// ClaimWebhook leases the next due delivery, or returns nil.
	ClaimWebhook(ctx context.Context) (*model.WebhookJob, error)
	// FinishWebhook records the result of a leased attempt. It returns
	// ErrWebhookLeaseLost when the lease is no longer held.
	FinishWebhook(ctx context.Context, job *model.WebhookJob, result model.WebhookResult) error
	// SweepWebhooks fails the deliveries out of attempts and removes the
	// deliveries finished before the given time.
	SweepWebhooks(ctx context.Context, finishedBefore time.Time) error
	WebhookStats(ctx context.Context) (model.WebhookStats, error)
}

// WebhookSender performs one signed attempt.
type WebhookSender interface {
	ValidateDestination(destination string) error
	SendWebhook(ctx context.Context, job *model.WebhookJob, secrets []string) model.WebhookResult
}
