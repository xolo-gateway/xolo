package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var WebhookAttempts = promauto.NewCounter(prometheus.CounterOpts{Namespace: Namespace, Name: "webhook_attempts_total", Help: "Webhook delivery attempts on this process"})
var WebhookErrors = promauto.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: "webhook_failures_total", Help: "Webhook failures by bounded diagnostic"}, []string{"reason"})
var WebhookQueue = promauto.NewGaugeVec(prometheus.GaugeOpts{Namespace: Namespace, Name: "webhook_queue", Help: "Database-wide retained webhook delivery rows by state"}, []string{"state"})
var WebhookLag = promauto.NewGaugeVec(prometheus.GaugeOpts{Namespace: Namespace, Name: "webhook_lag_seconds", Help: "Database-wide oldest outstanding webhook work age"}, []string{"stage"})
var WebhookHistoryLost = promauto.NewGauge(prometheus.GaugeOpts{Namespace: Namespace, Name: "webhook_history_lost", Help: "Subscriptions requiring explicit recovery after feed history loss"})
