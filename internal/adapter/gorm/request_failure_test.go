package gorm_test

import (
	"context"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

func TestUsageStore_RequestFailures(t *testing.T) {
	eachBackend(t, scenarioUsageStoreRequestFailures)
}

// scenarioUsageStoreRequestFailures asserts that a failure survives the round
// trip, trace included, that the query filters by organization and user, that
// the retention removes only what is due, and that no usage aggregate sees a
// failure.
func scenarioUsageStoreRequestFailures(t *testing.T, store *xologorm.Store) {
	ctx := context.Background()
	orgID, otherOrgID := model.NewOrgID(), model.NewOrgID()
	userA, userB := model.NewUserID(), model.NewUserID()

	upstream := 840 * time.Millisecond
	failed := model.NewRequestFailure()
	failed.UserID = userA
	failed.OrgID = orgID
	failed.ProviderID = model.NewProviderID()
	failed.ModelID = model.NewLLMModelID()
	failed.ProxyModelName = "fast"
	failed.ProviderKeyID = "k2"
	failed.HTTPStatus = 502
	failed.ErrorClass = "http_502"
	failed.Timing = model.UsageTiming{Upstream: &upstream}
	failed.Streamed = true
	failed.SessionID = "session-1"
	failed.Trace = &model.UsageTrace{Spans: []model.TraceSpan{
		{Kind: model.TraceSpanUpstreamAttempt, Name: "k1", Parent: -1, Status: model.TraceSpanError, HTTPStatus: 429, ErrorClass: "http_429"},
		{Kind: model.TraceSpanUpstreamAttempt, Name: "k2", Parent: -1, Status: model.TraceSpanError, HTTPStatus: 502, ErrorClass: "http_502"},
	}}

	other := model.NewRequestFailure()
	other.UserID = userB
	other.OrgID = orgID
	other.ErrorClass = "timeout"
	other.CreatedAt = failed.CreatedAt.Add(-time.Minute)

	elsewhere := model.NewRequestFailure()
	elsewhere.UserID = userA
	elsewhere.OrgID = otherOrgID
	elsewhere.ErrorClass = "error"

	for _, f := range []*model.RequestFailure{failed, other, elsewhere} {
		if err := store.RecordRequestFailure(ctx, f); err != nil {
			t.Fatalf("RecordRequestFailure: %+v", err)
		}
	}

	inOrg, err := store.QueryRequestFailures(ctx, port.RequestFailureFilter{OrgID: &orgID})
	if err != nil {
		t.Fatalf("QueryRequestFailures: %+v", err)
	}
	if len(inOrg) != 2 || inOrg[0].ID != failed.ID || inOrg[1].ID != other.ID {
		t.Fatalf("failures of the org = %+v, want the two of it, most recent first", inOrg)
	}

	got := inOrg[0]
	if got.HTTPStatus != 502 || got.ErrorClass != "http_502" || got.ProviderKeyID != "k2" || got.ProxyModelName != "fast" ||
		!got.Streamed || got.SessionID != "session-1" || got.ProviderID != failed.ProviderID || got.ModelID != failed.ModelID {
		t.Errorf("failure read back = %+v", got)
	}
	if got.Timing.Upstream == nil || *got.Timing.Upstream != upstream || got.Timing.Routing != nil {
		t.Errorf("timing = %+v, want upstream %s and the rest unknown", got.Timing, upstream)
	}
	if got.Trace == nil || len(got.Trace.Spans) != 2 || got.Trace.Spans[0].ErrorClass != "http_429" {
		t.Errorf("trace = %+v, want the two attempts", got.Trace)
	}
	if inOrg[1].Trace != nil {
		t.Errorf("a failure recorded without trace reads back %+v", inOrg[1].Trace)
	}

	byUser, err := store.QueryRequestFailures(ctx, port.RequestFailureFilter{OrgID: &orgID, UserID: &userB, Limit: ptr(10)})
	if err != nil {
		t.Fatalf("QueryRequestFailures (by user): %+v", err)
	}
	if len(byUser) != 1 || byUser[0].ID != other.ID {
		t.Errorf("failures of user B = %+v", byUser)
	}

	// A failure is neither usage nor cost: no aggregate of the records sees
	// it.
	aggregate, err := store.AggregateUsage(ctx, port.UsageFilter{OrgID: &orgID})
	if err != nil {
		t.Fatalf("AggregateUsage: %+v", err)
	}
	if aggregate.TotalRequests != 0 {
		t.Errorf("the usage aggregate counts %d requests, want none", aggregate.TotalRequests)
	}

	purged, err := store.PurgeRequestFailures(ctx, failed.CreatedAt.Add(-30*time.Second))
	if err != nil {
		t.Fatalf("PurgeRequestFailures: %+v", err)
	}
	if purged != 1 {
		t.Errorf("purged %d failures, want only the one recorded before the limit", purged)
	}
	left, err := store.QueryRequestFailures(ctx, port.RequestFailureFilter{OrgID: &orgID})
	if err != nil {
		t.Fatalf("QueryRequestFailures (after purge): %+v", err)
	}
	if len(left) != 1 || left[0].ID != failed.ID {
		t.Errorf("failures left = %+v, want the recent one", left)
	}
}
