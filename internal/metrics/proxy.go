package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	NameProxyRequestDuration    = "proxy_request_duration_seconds"
	NameProxyErrors             = "proxy_errors_total"
	NameFairShareDegradedShares = "fair_share_degraded_shares_total"
	NameStreamInterrupted       = "proxy_stream_interrupted_total"
	LabelModel                  = "model"
	LabelCause                  = "cause"
)

var ProxyRequestDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:      NameProxyRequestDuration,
		Help:      "Duration of LLM upstream requests in seconds",
		Namespace: Namespace,
		Buckets:   []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
	},
	[]string{LabelOrg, LabelModel},
)

var ProxyErrors = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:      NameProxyErrors,
		Help:      "Total number of failed LLM upstream requests",
		Namespace: Namespace,
	},
	[]string{LabelOrg},
)

// FairShareDegradedShares counts the per-user plan shares computed without the
// active-user count, because reading it failed. The share then falls back to
// the whole membership, which is safe but narrow; a rising counter means the
// usage table cannot answer the count and the plan is being under-allocated.
var FairShareDegradedShares = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:      NameFairShareDegradedShares,
		Help:      "Total number of subscription plan shares computed on a degraded active-user count",
		Namespace: Namespace,
	},
	[]string{LabelOrg},
)

// StreamInterrupted counts the streamed answers that stopped before the
// provider signalled completion, by cause: "upstream_error" when the provider
// failed mid-stream, "client_gone" when the client hung up. Those requests are
// billed by the provider and their usage is recorded, so this is the rate that
// sizes what an unstable provider costs — without it a provider failing
// mid-stream simply disappears from the figures.
var StreamInterrupted = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:      NameStreamInterrupted,
		Help:      "Total number of streamed responses interrupted before completion",
		Namespace: Namespace,
	},
	[]string{LabelOrg, LabelModel, LabelCause},
)
