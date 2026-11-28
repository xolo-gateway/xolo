package proxy

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bornholm/genai/llm"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// metaRequestTrace carries the *requestTrace of a request in its metadata,
// which every hook and the clients built for the request share.
const metaRequestTrace = "xolo.request.trace"

// requestTrace collects the spans of one proxy call as it runs. It keeps
// absolute times: the offsets are taken from the moment the proxy received the
// request, which only the usage tracker knows once the call is over.
type requestTrace struct {
	mu        sync.Mutex
	spans     []tracedSpan
	truncated bool
}

// tracedSpan is a span as it was recorded, with absolute times.
type tracedSpan struct {
	span  model.TraceSpan
	start time.Time
	end   time.Time
}

// requestTraceOf returns the trace of the request metadata belongs to,
// creating it on first use. A nil metadata has none.
func requestTraceOf(metadata map[string]any) *requestTrace {
	if metadata == nil {
		return nil
	}
	if trace, ok := metadata[metaRequestTrace].(*requestTrace); ok {
		return trace
	}
	trace := &requestTrace{}
	metadata[metaRequestTrace] = trace
	return trace
}

// existingRequestTrace returns the trace of a request, nil when nothing
// recorded a span for it.
func existingRequestTrace(metadata map[string]any) *requestTrace {
	trace, _ := metadata[metaRequestTrace].(*requestTrace)
	return trace
}

// add records a span that ran from start to end. The times of span are
// ignored: they are computed when the trace is built.
func (t *requestTrace) add(span model.TraceSpan, start, end time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.spans) >= model.MaxTraceSpans {
		t.truncated = true
		return
	}
	t.spans = append(t.spans, tracedSpan{span: span, start: start, end: end})
}

// len returns the number of spans recorded.
func (t *requestTrace) len() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.spans)
}

// usageTrace builds the trace kept with the usage record, its offsets taken
// from origin, the moment the request was received. An origin left zero takes
// the start of the first span.
func (t *requestTrace) usageTrace(origin time.Time) *model.UsageTrace {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.spans) == 0 {
		return nil
	}
	if origin.IsZero() {
		origin = t.spans[0].start
	}
	trace := &model.UsageTrace{Spans: make([]model.TraceSpan, len(t.spans)), Truncated: t.truncated}
	for i, recorded := range t.spans {
		span := recorded.span
		span.Start = recorded.start.Sub(origin)
		span.Duration = recorded.end.Sub(recorded.start)
		trace.Spans[i] = span
	}
	return trace
}

// upstreamAttemptSpan describes a call to a provider through key, ended by
// err.
func upstreamAttemptSpan(key model.ProviderKeyID, err error) model.TraceSpan {
	span := model.TraceSpan{
		Kind:   model.TraceSpanUpstreamAttempt,
		Name:   string(key),
		Parent: -1,
		Status: model.TraceSpanOK,
	}
	if err != nil {
		span.Status = model.TraceSpanError
		span.HTTPStatus, span.ErrorClass = classifyUpstreamError(err)
	}
	return span
}

// classifyUpstreamError sorts the failure of a call to a provider without
// keeping its message, which may quote the request or the provider's account.
func classifyUpstreamError(err error) (status int, class string) {
	var httpErr *llm.HTTPError
	switch {
	case errors.As(err, &httpErr):
		return httpErr.StatusCode, "http_" + strconv.Itoa(httpErr.StatusCode)
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout"
	case errors.Is(err, context.Canceled):
		return 0, "canceled"
	default:
		return 0, "error"
	}
}
