package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

func TestRequestTraceTakesOffsetsFromTheRequest(t *testing.T) {
	origin := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	trace := requestTraceOf(map[string]any{})
	trace.add(upstreamAttemptSpan("k1", planExhausted429), origin.Add(5*time.Millisecond), origin.Add(45*time.Millisecond))
	trace.add(upstreamAttemptSpan("k2", nil), origin.Add(45*time.Millisecond), origin.Add(300*time.Millisecond))

	got := trace.usageTrace(origin)

	require.False(t, got.Truncated)
	require.Len(t, got.Spans, 2)
	require.Equal(t, model.TraceSpan{
		Kind: model.TraceSpanUpstreamAttempt, Name: "k1", Parent: -1,
		Start: 5 * time.Millisecond, Duration: 40 * time.Millisecond,
		Status: model.TraceSpanError, HTTPStatus: http.StatusTooManyRequests, ErrorClass: "http_429",
	}, got.Spans[0])
	require.Equal(t, model.TraceSpanOK, got.Spans[1].Status)
	require.Equal(t, 45*time.Millisecond, got.Spans[1].Start)
	require.Equal(t, 255*time.Millisecond, got.Spans[1].Duration)
}

func TestRequestTraceIsBounded(t *testing.T) {
	trace := requestTraceOf(map[string]any{})
	now := time.Now()
	for i := 0; i < model.MaxTraceSpans+10; i++ {
		trace.add(upstreamAttemptSpan("k", nil), now, now)
	}

	got := trace.usageTrace(now)

	require.Len(t, got.Spans, model.MaxTraceSpans)
	require.True(t, got.Truncated, "a trace that dropped spans says so")
}

func TestRequestTraceIsSharedThroughTheMetadata(t *testing.T) {
	metadata := map[string]any{}
	require.Nil(t, existingRequestTrace(metadata), "nothing recorded, no trace")
	require.Same(t, requestTraceOf(metadata), requestTraceOf(metadata))
	require.Same(t, requestTraceOf(metadata), existingRequestTrace(metadata))
	require.Nil(t, requestTraceOf(nil))
	require.Nil(t, (*requestTrace)(nil).usageTrace(time.Now()), "a nil trace is safe to read")
}

func TestClassifyUpstreamError(t *testing.T) {
	for name, c := range map[string]struct {
		err    error
		status int
		class  string
	}{
		"http":     {&llm.HTTPError{StatusCode: http.StatusBadGateway}, http.StatusBadGateway, "http_502"},
		"wrapped":  {errors.Join(errors.New("context"), &llm.HTTPError{StatusCode: 429}), 429, "http_429"},
		"timeout":  {context.DeadlineExceeded, http.StatusGatewayTimeout, "timeout"},
		"canceled": {context.Canceled, 0, "canceled"},
		"other":    {errors.New("connection refused to 10.0.0.1"), 0, "error"},
	} {
		t.Run(name, func(t *testing.T) {
			status, class := classifyUpstreamError(c.err)
			require.Equal(t, c.status, status)
			require.Equal(t, c.class, class)
		})
	}
}

// TestKeyFailoverClientTracesEveryAttempt asserts that a request moved from
// one key to another records one upstream attempt per key it tried.
func TestKeyFailoverClientTracesEveryAttempt(t *testing.T) {
	first := &stubCompletionClient{err: planExhausted429}
	second := &stubCompletionClient{}
	metadata := map[string]any{}
	client := newKeyFailoverClient([]keyedClient{{"k1", first}, {"k2", second}}, metadata, nil)

	_, err := client.ChatCompletion(context.Background())
	require.NoError(t, err)

	trace := existingRequestTrace(metadata).usageTrace(time.Time{})
	require.Len(t, trace.Spans, 2)
	require.Equal(t, "k1", trace.Spans[0].Name)
	require.Equal(t, model.TraceSpanError, trace.Spans[0].Status)
	require.Equal(t, "http_429", trace.Spans[0].ErrorClass)
	require.Equal(t, "k2", trace.Spans[1].Name)
	require.Equal(t, model.TraceSpanOK, trace.Spans[1].Status)
}

// TestUsageTrackerRecordsTheTimeline asserts that the timing, finish reason,
// streaming flag and session the proxy measured reach the usage record, and
// that the trace is kept only when there was more than one attempt.
func TestUsageTrackerRecordsTheTimeline(t *testing.T) {
	orgID := model.OrgID("org-1")
	provider := model.NewProvider(orgID, "openrouter", "openrouter", "https://openrouter.ai", "key", "USD")
	llmModel := model.NewLLMModel(provider.ID(), orgID, "cadoles/test-model", "real/model", "desc", 1000, 2000)
	org := model.NewOrganization(testTenantID, "org-1", "Org 1", "", "USD")
	received := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

	for name, attempts := range map[string]int{"one attempt": 1, "two attempts": 2} {
		t.Run(name, func(t *testing.T) {
			tracker, usageStore := newTestTracker(&fakeProviderStore{provider: provider, llmModel: llmModel}, &fakeOrgStore{org: org})
			req, res := newTestRequestResponse(llmModel.ID(), orgID, &genaiProxy.TokenUsage{PromptTokens: 100, CompletionTokens: 244, TotalTokens: 344})
			req.Stream = true
			req.SessionID = "session-1"
			req.Timing = genaiProxy.ExchangeTiming{
				ReceivedAt:      received,
				UpstreamStartAt: received.Add(7 * time.Millisecond),
				FirstOutputAt:   received.Add(183 * time.Millisecond),
				CompletedAt:     received.Add(1183 * time.Millisecond),
			}
			res.FinishReason = llm.FinishReasonLength
			trace := requestTraceOf(req.Metadata)
			for i := 0; i < attempts; i++ {
				trace.add(upstreamAttemptSpan("k", nil), received.Add(7*time.Millisecond), received.Add(20*time.Millisecond))
			}

			_, err := tracker.PostResponse(context.Background(), req, res)
			require.NoError(t, err)

			recorded := usageStore.recorded
			require.NotNil(t, recorded)
			timing := recorded.Timing()
			require.Equal(t, 7*time.Millisecond, *timing.Routing)
			require.Equal(t, 176*time.Millisecond, *timing.FirstOutput)
			require.Equal(t, 1176*time.Millisecond, *timing.Upstream)
			throughput, ok := model.Throughput(timing, recorded.CompletionTokens())
			require.True(t, ok)
			require.InDelta(t, 244, throughput, 0.001)
			require.Equal(t, "length", recorded.FinishReason())
			require.True(t, recorded.Streamed())
			require.Equal(t, "session-1", recorded.SessionID())

			kept := recorded.(model.UsageTraceCarrier).Trace()
			if attempts < 2 {
				require.Nil(t, kept, "a single attempt is the record's own timing")
				return
			}
			require.Len(t, kept.Spans, 2)
			require.Equal(t, 7*time.Millisecond, kept.Spans[0].Start, "offsets are taken from the request's receipt")
		})
	}
}

// TestUsageTrackerLeavesUnknownStagesUnknown asserts that a call whose stages
// the proxy did not measure, such as one that was not streamed, records them
// as unknown rather than as zero.
func TestUsageTrackerLeavesUnknownStagesUnknown(t *testing.T) {
	orgID := model.OrgID("org-1")
	provider := model.NewProvider(orgID, "openrouter", "openrouter", "https://openrouter.ai", "key", "USD")
	llmModel := model.NewLLMModel(provider.ID(), orgID, "cadoles/test-model", "real/model", "desc", 1000, 2000)
	org := model.NewOrganization(testTenantID, "org-1", "Org 1", "", "USD")
	tracker, usageStore := newTestTracker(&fakeProviderStore{provider: provider, llmModel: llmModel}, &fakeOrgStore{org: org})
	req, res := newTestRequestResponse(llmModel.ID(), orgID, &genaiProxy.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	received := time.Now()
	req.Timing = genaiProxy.ExchangeTiming{ReceivedAt: received, UpstreamStartAt: received, CompletedAt: received.Add(time.Second)}

	_, err := tracker.PostResponse(context.Background(), req, res)
	require.NoError(t, err)

	timing := usageStore.recorded.Timing()
	require.NotNil(t, timing.Upstream)
	require.Nil(t, timing.FirstOutput, "an answer that was not streamed has no first output")
	_, ok := model.Throughput(timing, 5)
	require.False(t, ok)
}

// TestPipelineWrappersKeepTheFinishReason asserts that wrapping a response or
// a chunk to rewrite its content does not hide why the model stopped.
func TestPipelineWrappersKeepTheFinishReason(t *testing.T) {
	response := &wrappedChatCompletionResponse{
		inner: llm.NewChatCompletionResponse(llm.NewMessage(llm.RoleAssistant, "Hel"), llm.NewChatCompletionUsage(1, 1, 2)).
			WithFinishReason(llm.FinishReasonLength),
		modifiedContent: "rewritten",
	}
	require.Equal(t, llm.FinishReasonLength, llm.FinishReasonOf(response))

	chunk := &contentOverrideChunk{
		StreamChunk: llm.NewCompleteStreamChunk(llm.NewChatCompletionUsage(1, 1, 2)).WithFinishReason(llm.FinishReasonContentFilter),
		delta:       llm.NewStreamDelta(llm.RoleAssistant, ""),
	}
	require.Equal(t, llm.FinishReasonContentFilter, llm.FinishReasonOf(chunk))
}

// failureFixture returns a tracker and a request that reached its provider.
func failureFixture(t *testing.T) (*XoloUsageTracker, *fakeUsageStore, *genaiProxy.ProxyRequest, model.LLMModel) {
	t.Helper()
	orgID := model.OrgID("org-1")
	provider := model.NewProvider(orgID, "openrouter", "openrouter", "https://openrouter.ai", "key", "USD")
	llmModel := model.NewLLMModel(provider.ID(), orgID, "cadoles/test-model", "real/model", "desc", 1000, 2000)
	org := model.NewOrganization(testTenantID, "org-1", "Org 1", "", "USD")
	tracker, usageStore := newTestTracker(&fakeProviderStore{provider: provider, llmModel: llmModel}, &fakeOrgStore{org: org})
	req, _ := newTestRequestResponse(llmModel.ID(), orgID, nil)
	received := time.Now()
	req.Timing = genaiProxy.ExchangeTiming{
		ReceivedAt:      received,
		UpstreamStartAt: received.Add(4 * time.Millisecond),
		CompletedAt:     received.Add(904 * time.Millisecond),
	}
	req.Stream = true
	return tracker, usageStore, req, llmModel
}

// TestUsageTrackerRecordsAProviderFailure asserts that a call the provider
// failed is recorded as a failure, with what the request logs need, and never
// as usage.
func TestUsageTrackerRecordsAProviderFailure(t *testing.T) {
	tracker, usageStore, req, llmModel := failureFixture(t)
	req.Metadata[MetaProviderKeyUsed] = model.ProviderKeyID("k2")

	result, err := tracker.OnError(context.Background(), req, &llm.HTTPError{StatusCode: http.StatusBadGateway, Body: "upstream said something private"})
	require.NoError(t, err)
	require.Nil(t, result, "the tracker never answers in the client's stead")

	require.Nil(t, usageStore.recorded, "a failure is not usage")
	require.Len(t, usageStore.failures, 1)
	failure := usageStore.failures[0]
	require.Equal(t, model.OrgID("org-1"), failure.OrgID)
	require.Equal(t, model.UserID("user-1"), failure.UserID)
	require.Equal(t, llmModel.ID(), failure.ModelID)
	require.Equal(t, llmModel.ProviderID(), failure.ProviderID)
	require.Equal(t, model.ProviderKeyID("k2"), failure.ProviderKeyID)
	require.Equal(t, http.StatusBadGateway, failure.HTTPStatus)
	require.Equal(t, "http_502", failure.ErrorClass)
	require.Equal(t, 900*time.Millisecond, *failure.Timing.Upstream)
	require.True(t, failure.Streamed)
}

// TestUsageTrackerIgnoresWhatIsNotAProviderFailure asserts that a refusal of
// the gateway itself, before the upstream call, and a client hanging up are
// not recorded as failures of the provider.
func TestUsageTrackerIgnoresWhatIsNotAProviderFailure(t *testing.T) {
	t.Run("refused before the upstream call", func(t *testing.T) {
		tracker, usageStore, req, _ := failureFixture(t)
		req.Timing.UpstreamStartAt = time.Time{}
		req.Timing.CompletedAt = time.Time{}

		_, err := tracker.OnError(context.Background(), req, errors.New("quota exceeded"))
		require.NoError(t, err)
		require.Empty(t, usageStore.failures)
	})
	t.Run("client gone", func(t *testing.T) {
		tracker, usageStore, req, _ := failureFixture(t)

		_, err := tracker.OnError(context.Background(), req, context.Canceled)
		require.NoError(t, err)
		require.Empty(t, usageStore.failures)
	})
}

// TestUsageTrackerRecordsAFailureOnACanceledRequest asserts that the failure is
// written even when the request's context is already canceled, as it is when
// the client left while the provider was failing.
func TestUsageTrackerRecordsAFailureOnACanceledRequest(t *testing.T) {
	tracker, usageStore, req, _ := failureFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tracker.OnError(ctx, req, &llm.HTTPError{StatusCode: http.StatusServiceUnavailable})
	require.NoError(t, err)
	require.Len(t, usageStore.failures, 1)
	require.NoError(t, usageStore.failureCtxErr, "the write is detached from the request's cancellation")
}
