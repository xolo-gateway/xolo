package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	NameWebhookAttempts     = "webhook_attempts_total"
	NameWebhookFailures     = "webhook_failures_total"
	NameWebhookQueue        = "webhook_queue"
	NameWebhookLag          = "webhook_lag_seconds"
	NameWebhookHistoryLost  = "webhook_history_lost"
	NameWebhookBackpressure = "webhook_backpressure"

	LabelWebhookReason = "reason"
	LabelWebhookState  = "state"
	LabelWebhookStage  = "stage"
)

var WebhookAttempts = promauto.NewCounter(
	prometheus.CounterOpts{
		Name:      NameWebhookAttempts,
		Help:      "Webhook delivery attempts made by this process",
		Namespace: Namespace,
	},
)

var WebhookFailures = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:      NameWebhookFailures,
		Help:      "Webhook failures of this process, by bounded diagnostic",
		Namespace: Namespace,
	},
	[]string{LabelWebhookReason},
)

var WebhookQueue = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name:      NameWebhookQueue,
		Help:      "Webhook deliveries retained by the database, by state",
		Namespace: Namespace,
	},
	[]string{LabelWebhookState},
)

var WebhookLag = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name:      NameWebhookLag,
		Help:      "Age of the oldest outstanding webhook work, by stage",
		Namespace: Namespace,
	},
	[]string{LabelWebhookStage},
)

var WebhookHistoryLost = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name:      NameWebhookHistoryLost,
		Help:      "Webhook subscriptions waiting for a reset after the feed retention passed them",
		Namespace: Namespace,
	},
)

var WebhookBackpressure = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name:      NameWebhookBackpressure,
		Help:      "Webhook subscriptions held back by the queue capacity",
		Namespace: Namespace,
	},
)
