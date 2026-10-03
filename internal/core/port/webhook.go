package port

import (
	"context"
	"errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

var ErrWebhookLeaseLost = errors.New("webhook lease lost")
var ErrWebhookCapacity = errors.New("webhook capacity reached")

type WebhookStore interface {
	ListWebhookDeliveries(context.Context, string, string) ([]model.WebhookDeliveryStatus, error)
	PutWebhook(context.Context, string, string, model.WebhookSettings) (model.WebhookSubscription, error)
	GetWebhook(context.Context, string, string) (model.WebhookSubscription, error)
	ListWebhooks(context.Context, string) ([]model.WebhookSubscription, error)
	DeleteWebhook(context.Context, string, string) error
	ResetWebhook(context.Context, string, string) error
	PrepareWebhooks(context.Context, int) error
	ClaimWebhook(context.Context) (*model.WebhookJob, error)
	FinishWebhook(context.Context, *model.WebhookJob, model.WebhookResult) error
	WebhookStats(context.Context) (model.WebhookStats, error)
}
type WebhookSender interface {
	ValidateDestination(string) error
	SendWebhook(context.Context, *model.WebhookJob, []string) model.WebhookResult
}
