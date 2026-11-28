package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	NameProxyRequestDuration      = "proxy_request_duration_seconds"
	NameProxyErrors               = "proxy_errors_total"
	NameFairShareDegradedShares   = "fair_share_degraded_shares_total"
	NamePlanModelGroupingFailures = "plan_model_grouping_failures_total"
	NameStreamInterrupted         = "proxy_stream_interrupted_total"
	LabelModel                    = "model"
	LabelCause                    = "cause"
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

// PlanModelGroupingFailures counts the requests to a per-model or shared
// subscription plan for which the organization's models could not be listed.
// A per-model constraint then meters the requested model alone, which
// undercounts an upstream model published under several proxy names; a shared
// one weighs every model at the constraint's highest rate, which closes the
// window early.
var PlanModelGroupingFailures = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:      NamePlanModelGroupingFailures,
		Help:      "Total number of per-model or shared plan checks metered without the model list because it could not be read",
		Namespace: Namespace,
	},
	[]string{LabelOrg},
)

// StreamInterrupted counts the streamed answers that stopped before the
// provider signalled completion, by cause: "upstream_error" when the provider
// failed mid-stream, "write_failed" when writing the response failed for a
// reason of our own, "client_gone" when the client hung up, "stream_truncated"
// when the provider closed without signalling the end. The first two are
// faults, the last two ordinary traffic. Those requests are billed by the
// provider and their usage is recorded, so this is the rate that sizes what an
// unstable provider costs — without it a provider failing mid-stream simply
// disappears from the figures.
var StreamInterrupted = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name:      NameStreamInterrupted,
		Help:      "Total number of streamed responses interrupted before completion",
		Namespace: Namespace,
	},
	[]string{LabelOrg, LabelModel, LabelCause},
)

const (
	NameProxyTimeToFirstOutput = "proxy_time_to_first_output_seconds"
	NameProxyOutputThroughput  = "proxy_output_tokens_per_second"
	// LabelProvider is the name of the provider a request went through.
	LabelProvider = "provider"
)

// ProxyTimeToFirstOutput is the time between the call to a provider and the
// first streamed chunk carrying output: the latency a streaming client
// perceives from the provider, the gateway's own work left out. Only streamed
// answers are observed.
var ProxyTimeToFirstOutput = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:      NameProxyTimeToFirstOutput,
		Help:      "Time between the call to the provider and the first streamed output, in seconds",
		Namespace: Namespace,
		Buckets:   []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
	},
	[]string{LabelOrg, LabelProvider, LabelModel},
)

// ProxyOutputThroughput is the output tokens per second of a streamed answer,
// over the time the provider spent producing it. Answers whose token counts
// are estimated are not observed: an interrupted stream would skew it.
var ProxyOutputThroughput = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:      NameProxyOutputThroughput,
		Help:      "Output tokens per second of streamed answers",
		Namespace: Namespace,
		Buckets:   []float64{5, 10, 20, 40, 80, 160, 320, 640},
	},
	[]string{LabelOrg, LabelProvider, LabelModel},
)
