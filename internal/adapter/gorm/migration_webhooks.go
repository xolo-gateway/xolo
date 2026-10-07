package gorm

import (
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

const webhooksMigrationID = "202610090001"

// webhookFeedIndex serves the per-tenant reads of the feed by the webhooks.
const webhookFeedIndex = "idx_provisioning_events_tenant_sequence"

// migrateWebhooks creates the subscriptions and their delivery queue. Existing
// events are never delivered: a subscription starts at the end of the feed.
func migrateWebhooks(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&WebhookSubscription{}, &WebhookDelivery{}); err != nil {
		return errors.WithStack(err)
	}
	if tx.Migrator().HasIndex(&ProvisioningEvent{}, webhookFeedIndex) {
		return nil
	}
	return errors.WithStack(tx.Migrator().CreateIndex(&ProvisioningEvent{}, webhookFeedIndex))
}
