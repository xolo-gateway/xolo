package gorm_test

import (
	"context"
	"testing"
	"time"

	"github.com/pkg/errors"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

func TestUsageStore_RequestTimeline(t *testing.T) {
	eachBackend(t, scenarioUsageStoreRequestTimeline)
}

// scenarioUsageStoreRequestTimeline asserts that the timing, finish reason,
// streaming flag and session of a request survive the round trip, that its
// trace is written with it, and that a record written without them reads back
// with every stage unknown.
func scenarioUsageStoreRequestTimeline(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()
	orgID, userID := model.NewOrgID(), model.NewUserID()

	routing, firstOutput, upstream := 3*time.Millisecond, 250*time.Millisecond, 1250*time.Millisecond
	traced := model.NewUsageRecord(userID, "", orgID, "", model.NewProviderID(), model.NewLLMModelID(),
		"fast", "", 100, 0, 244, 1_000, "USD", model.CostSourceComputed, "")
	traced.SetTiming(model.UsageTiming{Routing: &routing, FirstOutput: &firstOutput, Upstream: &upstream})
	traced.SetFinishReason("length")
	traced.SetStreamed(true)
	traced.SetSessionID("session-1")
	traced.SetTrace(&model.UsageTrace{Spans: []model.TraceSpan{
		{Kind: model.TraceSpanUpstreamAttempt, Name: "key-a", Parent: -1, Start: 3 * time.Millisecond, Duration: 40 * time.Millisecond,
			Status: model.TraceSpanError, HTTPStatus: 429, ErrorClass: "http_429"},
		{Kind: model.TraceSpanUpstreamAttempt, Name: "key-b", Parent: -1, Start: 43 * time.Millisecond, Duration: 210 * time.Millisecond,
			Status: model.TraceSpanOK, Attributes: map[string]string{"note": "kept"}},
	}})

	// A subscription-covered record goes through the longest combined
	// statement on PostgreSQL: the trace must fit in it as well.
	planned := model.NewUsageRecord(userID, "", orgID, "", model.NewProviderID(), model.NewLLMModelID(),
		"fast", "", 10, 0, 5, 0, "USD", model.CostSourceComputed, "")
	planned.SetPlanCovered(true)
	planned.SetTrace(&model.UsageTrace{Truncated: true, Spans: []model.TraceSpan{
		{Kind: model.TraceSpanUpstreamAttempt, Name: "key-a", Parent: -1, Status: model.TraceSpanOK},
	}})

	plain := model.NewUsageRecord(userID, "", orgID, "", model.NewProviderID(), model.NewLLMModelID(),
		"fast", "", 10, 0, 5, 100, "USD", model.CostSourceComputed, "")

	for _, record := range []*model.BaseUsageRecord{traced, planned, plain} {
		if err := store.RecordUsage(ctx, record); err != nil {
			t.Fatalf("RecordUsage: %+v", err)
		}
	}

	records, err := store.QueryUsage(ctx, port.UsageFilter{OrgID: &orgID})
	if err != nil {
		t.Fatalf("QueryUsage: %v", err)
	}
	byID := map[model.UsageRecordID]model.UsageRecord{}
	for _, record := range records {
		byID[record.ID()] = record
	}

	got := byID[traced.ID()]
	if got == nil {
		t.Fatal("the traced record was not read back")
	}
	timing := got.Timing()
	if timing.Routing == nil || *timing.Routing != routing ||
		timing.FirstOutput == nil || *timing.FirstOutput != firstOutput ||
		timing.Upstream == nil || *timing.Upstream != upstream {
		t.Errorf("timing = %+v, want routing %s, first output %s, upstream %s", timing, routing, firstOutput, upstream)
	}
	if got.FinishReason() != "length" || !got.Streamed() || got.SessionID() != "session-1" {
		t.Errorf("finish reason %q, streamed %v, session %q", got.FinishReason(), got.Streamed(), got.SessionID())
	}
	if throughput, ok := model.Throughput(got.Timing(), got.CompletionTokens()); !ok || throughput != 244 {
		t.Errorf("throughput = %v, %v; want 244 tokens/s", throughput, ok)
	}

	unknown := byID[plain.ID()].Timing()
	if unknown.Routing != nil || unknown.FirstOutput != nil || unknown.Upstream != nil {
		t.Errorf("a record written without timing reads back %+v, want every stage unknown", unknown)
	}

	trace, err := store.GetUsageTrace(ctx, traced.ID())
	if err != nil {
		t.Fatalf("GetUsageTrace: %+v", err)
	}
	if trace.Truncated || len(trace.Spans) != 2 {
		t.Fatalf("trace = %+v, want the two spans recorded", trace)
	}
	first, second := trace.Spans[0], trace.Spans[1]
	if first.Name != "key-a" || first.Status != model.TraceSpanError || first.HTTPStatus != 429 || first.ErrorClass != "http_429" ||
		first.Start != 3*time.Millisecond || first.Duration != 40*time.Millisecond || first.Parent != -1 {
		t.Errorf("first span = %+v", first)
	}
	if second.Name != "key-b" || second.Status != model.TraceSpanOK || second.Attributes["note"] != "kept" {
		t.Errorf("second span = %+v", second)
	}

	plannedTrace, err := store.GetUsageTrace(ctx, planned.ID())
	if err != nil {
		t.Fatalf("GetUsageTrace (plan covered): %+v", err)
	}
	if !plannedTrace.Truncated || len(plannedTrace.Spans) != 1 {
		t.Errorf("plan-covered trace = %+v, want one span, truncated", plannedTrace)
	}

	if _, err := store.GetUsageTrace(ctx, plain.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("GetUsageTrace of a record without trace: err = %v, want ErrNotFound", err)
	}
}

func TestUsageStore_PurgeUsageTraces(t *testing.T) {
	eachBackend(t, scenarioUsageStorePurgeUsageTraces)
}

// scenarioUsageStorePurgeUsageTraces asserts that the retention removes the
// traces recorded before its limit, and only them, and leaves the usage
// records the costs are read from.
func scenarioUsageStorePurgeUsageTraces(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()
	orgID := model.NewOrgID()

	record := model.NewUsageRecord(model.NewUserID(), "", orgID, "", model.NewProviderID(), model.NewLLMModelID(),
		"fast", "", 10, 0, 5, 100, "USD", model.CostSourceComputed, "")
	record.SetTrace(&model.UsageTrace{Spans: []model.TraceSpan{
		{Kind: model.TraceSpanUpstreamAttempt, Name: "key-a", Parent: -1, Status: model.TraceSpanOK},
	}})
	if err := store.RecordUsage(ctx, record); err != nil {
		t.Fatalf("RecordUsage: %+v", err)
	}

	purged, err := store.PurgeUsageTraces(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("PurgeUsageTraces (none due): %+v", err)
	}
	if purged != 0 {
		t.Errorf("purged %d traces recorded after the limit, want 0", purged)
	}
	if _, err := store.GetUsageTrace(ctx, record.ID()); err != nil {
		t.Fatalf("the trace is gone before its limit: %+v", err)
	}

	purged, err = store.PurgeUsageTraces(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("PurgeUsageTraces: %+v", err)
	}
	if purged != 1 {
		t.Errorf("purged %d traces, want 1", purged)
	}
	if _, err := store.GetUsageTrace(ctx, record.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("GetUsageTrace after purge: err = %v, want ErrNotFound", err)
	}

	records, err := store.QueryUsage(ctx, port.UsageFilter{OrgID: &orgID})
	if err != nil {
		t.Fatalf("QueryUsage: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("the purge removed usage records: %d left, want 1", len(records))
	}
}
