package model

import "time"

// TraceSpanKind tells what a span of a usage trace measures.
type TraceSpanKind string

const (
	// TraceSpanUpstreamAttempt is one call to a provider: a request moved to
	// another API key of the provider after an exhaustion makes one per key
	// tried. For a stream it covers the opening of the stream, up to the
	// provider's answer or refusal, not the generation that follows.
	TraceSpanUpstreamAttempt TraceSpanKind = "upstream_attempt"
)

// TraceSpanStatus tells how the operation a span measures ended.
type TraceSpanStatus string

const (
	TraceSpanOK    TraceSpanStatus = "ok"
	TraceSpanError TraceSpanStatus = "error"
)

// MaxTraceSpans bounds the spans a usage trace keeps. The spans past it are
// dropped and the trace marked truncated: a request looping on a provider must
// not grow its trace without bound.
const MaxTraceSpans = 256

// TraceSpan is one timed operation of a proxy call.
type TraceSpan struct {
	Kind TraceSpanKind
	// Name says which operation it is, the API key of an upstream attempt for
	// one.
	Name string
	// Parent is the index of the enclosing span in the trace, -1 for a span
	// at the top.
	Parent int
	// Start is the offset of the span from the moment the request was
	// received.
	Start    time.Duration
	Duration time.Duration
	Status   TraceSpanStatus
	// HTTPStatus is the status the provider answered, zero when unknown or
	// when it did not answer.
	HTTPStatus int
	// ErrorClass sorts a failure without carrying its message, which may hold
	// what the request or the provider's account contained: "http_429",
	// "timeout", "canceled", "error".
	ErrorClass string
	// Attributes are other facts about the operation, as strings. They never
	// carry request or response content.
	Attributes map[string]string
}

// UsageTrace is the spans recorded during one proxy call, kept beside its
// usage record when they say more than the record does: several upstream
// attempts, or the nodes of a pipeline.
type UsageTrace struct {
	Spans []TraceSpan
	// Truncated marks a trace that reached MaxTraceSpans and dropped the
	// spans past it.
	Truncated bool
}

// UsageTraceCarrier is implemented by the usage records that carry the trace
// of their request on their way to the store. A record read back does not:
// the trace is read on its own, see port.UsageStore.GetUsageTrace.
type UsageTraceCarrier interface {
	Trace() *UsageTrace
}
