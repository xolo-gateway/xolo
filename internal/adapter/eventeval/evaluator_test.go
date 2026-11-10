package eventeval_test

import (
	"context"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/adapter/eventeval"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	gormpkg "gorm.io/gorm"
)

// These tests evaluate the same alert back to back: a nanosecond interval
// leaves the claim nothing to refuse (see TestEvaluator_OneInstanceFires).

func newStore(t *testing.T) *xologorm.Store {
	t.Helper()
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return xologorm.NewStore(db)
}

func recordN(t *testing.T, store *xologorm.Store, ctx context.Context, org model.OrgID, typ string, n int) {
	t.Helper()
	for range n {
		e := model.NewEvent(model.EventSourcePlatform, typ, model.WithEventOrg(org))
		if err := store.RecordEvent(ctx, e); err != nil {
			t.Fatalf("RecordEvent: %v", err)
		}
	}
}

func TestEvaluator_StateMachine(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	org := model.OrgID("org1")

	// Alert: count over 1h of auth failures > 2, no dwell.
	alert := model.NewAlert(org, "u1", "brute-force",
		model.WithAlertQuery(`{type="auth.login.failed"}`),
		model.WithAlertWindow(time.Hour),
		model.WithAlertComparator(model.ComparatorGT),
		model.WithAlertThreshold(2),
		model.WithAlertFor(0),
		model.WithAlertEnabled(true),
	)
	if err := store.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}

	ev := eventeval.NewEvaluator(store, store, store, time.Nanosecond)

	// Only 2 events → threshold not exceeded → ok.
	recordN(t, store, ctx, org, model.EventTypeAuthLoginFailed, 2)
	evaluateOnce(t, ev, ctx)
	assertState(t, store, alert.ID(), model.AlertStateOK)

	// Add 2 more (total 4) → firing.
	recordN(t, store, ctx, org, model.EventTypeAuthLoginFailed, 2)
	evaluateOnce(t, ev, ctx)
	assertState(t, store, alert.ID(), model.AlertStateFiring)

	// One firing incident must exist with pinned events.
	orgID := org
	incidents, err := store.ListIncidents(ctx, port.IncidentFilter{OrgID: &orgID})
	if err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
	pinned, err := store.ListIncidentEvents(ctx, incidents[0].ID())
	if err != nil {
		t.Fatalf("ListIncidentEvents: %v", err)
	}
	if len(pinned) != 4 {
		t.Fatalf("expected 4 pinned events, got %d", len(pinned))
	}

	// Re-evaluate while firing → still firing, no duplicate incident.
	evaluateOnce(t, ev, ctx)
	incidents, _ = store.ListIncidents(ctx, port.IncidentFilter{OrgID: &orgID})
	if len(incidents) != 1 {
		t.Fatalf("expected still 1 incident, got %d", len(incidents))
	}
}

func recordUserN(t *testing.T, store *xologorm.Store, ctx context.Context, org model.OrgID, userID model.UserID, typ string, n int) {
	t.Helper()
	for range n {
		e := model.NewEvent(model.EventSourcePlatform, typ, model.WithEventOrg(org), model.WithEventUser(userID))
		if err := store.RecordEvent(ctx, e); err != nil {
			t.Fatalf("RecordEvent: %v", err)
		}
	}
}

func TestEvaluator_PersonalScopeCountsOnlyOwnerEvents(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	org := model.OrgID("org3")
	owner := model.UserID("owner-user")

	alert := model.NewAlert(org, owner, "personal",
		model.WithAlertScope(model.AlertScopePersonal),
		model.WithAlertQuery(`{type="proxy.request"}`),
		model.WithAlertWindow(time.Hour),
		model.WithAlertComparator(model.ComparatorGT),
		model.WithAlertThreshold(1),
		model.WithAlertEnabled(true),
	)
	if err := store.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	ev := eventeval.NewEvaluator(store, store, store, time.Nanosecond)

	// 1 owner event and 5 from another user: only the owner's 1 counts → 1 > 1 is
	// false → OK (the other user's events must not leak into a personal alert).
	recordUserN(t, store, ctx, org, owner, model.EventTypeProxyRequest, 1)
	recordUserN(t, store, ctx, org, model.UserID("other-user"), model.EventTypeProxyRequest, 5)
	evaluateOnce(t, ev, ctx)
	assertState(t, store, alert.ID(), model.AlertStateOK)

	// Two more owner events → 3 > 1 → firing.
	recordUserN(t, store, ctx, org, owner, model.EventTypeProxyRequest, 2)
	evaluateOnce(t, ev, ctx)
	assertState(t, store, alert.ID(), model.AlertStateFiring)
}

func TestEvaluator_Resolve(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	org := model.OrgID("org2")

	// Window is stored with second granularity, so use 1s (sub-second windows
	// are not meaningful for alerts).
	alert := model.NewAlert(org, "u1", "short-window",
		model.WithAlertQuery(`{type="proxy.request"}`),
		model.WithAlertWindow(time.Second),
		model.WithAlertComparator(model.ComparatorGT),
		model.WithAlertThreshold(0),
		model.WithAlertEnabled(true),
	)
	if err := store.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}

	ev := eventeval.NewEvaluator(store, store, store, time.Nanosecond)

	recordN(t, store, ctx, org, model.EventTypeProxyRequest, 1)
	evaluateOnce(t, ev, ctx)
	assertState(t, store, alert.ID(), model.AlertStateFiring)

	// Let the event age out of the window, then re-evaluate → resolved.
	time.Sleep(1200 * time.Millisecond)
	evaluateOnce(t, ev, ctx)
	assertState(t, store, alert.ID(), model.AlertStateOK)

	orgID := org
	resolved := model.IncidentStatusResolved
	incidents, err := store.ListIncidents(ctx, port.IncidentFilter{OrgID: &orgID, Status: &resolved})
	if err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	if len(incidents) != 1 || incidents[0].ResolvedAt() == nil {
		t.Fatalf("expected 1 resolved incident with resolvedAt set")
	}
}

// evaluateOnce runs a single evaluation pass by invoking the exported evaluation
// via a short-lived Run cancelled immediately after the first tick is not
// possible; instead we call the unexported path through a tiny helper.
func evaluateOnce(t *testing.T, ev *eventeval.Evaluator, ctx context.Context) {
	t.Helper()
	ev.EvaluateAllForTest(ctx)
}

func assertState(t *testing.T, store *xologorm.Store, id model.AlertID, want model.AlertState) {
	t.Helper()
	a, err := store.GetAlertByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAlertByID: %v", err)
	}
	if a.State() != want {
		t.Fatalf("expected state %q, got %q", want, a.State())
	}
}

// staleAlerts returns the alerts as they were listed before any instance
// evaluated them, like an instance whose pass started at the same time as
// another one's.
type staleAlerts struct {
	*xologorm.Store
	snapshot []model.Alert
}

func (s staleAlerts) ListEnabledAlerts(context.Context) ([]model.Alert, error) {
	return s.snapshot, nil
}

// Two instances listing the alerts at the same time both see it ok: without
// the claim, each would open its own incident for the same crossing.
func TestEvaluator_OneInstanceFires(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	org := model.OrgID("org4")

	alert := model.NewAlert(org, "u1", "shared",
		model.WithAlertQuery(`{type="proxy.request"}`),
		model.WithAlertWindow(time.Hour),
		model.WithAlertComparator(model.ComparatorGT),
		model.WithAlertThreshold(0),
		model.WithAlertEnabled(true),
	)
	if err := store.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	recordN(t, store, ctx, org, model.EventTypeProxyRequest, 1)

	snapshot, err := store.ListEnabledAlerts(ctx)
	if err != nil {
		t.Fatalf("ListEnabledAlerts: %v", err)
	}
	listed := staleAlerts{Store: store, snapshot: snapshot}

	first := eventeval.NewEvaluator(listed, store, store, time.Minute)
	second := eventeval.NewEvaluator(listed, store, store, time.Minute)
	first.EvaluateAllForTest(ctx)
	second.EvaluateAllForTest(ctx)

	assertState(t, store, alert.ID(), model.AlertStateFiring)
	orgID := org
	incidents, err := store.ListIncidents(ctx, port.IncidentFilter{OrgID: &orgID})
	if err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
}

// blockingEvents holds the first evaluation inside its event query until told
// to go on, so another instance evaluates the same alert meanwhile.
type blockingEvents struct {
	*xologorm.Store
	entered chan struct{}
	release chan struct{}
}

func (s blockingEvents) QueryEvents(ctx context.Context, filter port.EventFilter) ([]model.Event, error) {
	close(s.entered)
	<-s.release
	return s.Store.QueryEvents(ctx, filter)
}

// An evaluation outlasting the claim lets a second instance claim and fire the
// same crossing while the first is still evaluating: the first must then
// find the incident open instead of opening another one.
func TestEvaluator_OverlappingEvaluationsOpenOneIncident(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	// One connection: an in-memory SQLite database is per connection.
	sqlDB.SetMaxOpenConns(1)
	store := xologorm.NewStore(db)
	ctx := context.Background()
	org := model.OrgID("org5")

	alert := model.NewAlert(org, "u1", "slow",
		model.WithAlertQuery(`{type="proxy.request"}`),
		model.WithAlertWindow(time.Hour),
		model.WithAlertComparator(model.ComparatorGT),
		model.WithAlertThreshold(0),
		model.WithAlertEnabled(true),
	)
	if err := store.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	recordN(t, store, ctx, org, model.EventTypeProxyRequest, 1)

	blocking := blockingEvents{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	slow := eventeval.NewEvaluator(store, store, blocking, time.Nanosecond)
	fast := eventeval.NewEvaluator(store, store, store, time.Nanosecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		slow.EvaluateAllForTest(ctx)
	}()
	select {
	case <-blocking.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first evaluation never reached its event query")
	}
	fast.EvaluateAllForTest(ctx)
	close(blocking.release)
	<-done

	assertState(t, store, alert.ID(), model.AlertStateFiring)
	orgID := org
	incidents, err := store.ListIncidents(ctx, port.IncidentFilter{OrgID: &orgID})
	if err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
}

// An alert deleted between the listing and the reload is skipped, not
// dereferenced.
func TestEvaluator_AlertDeletedAfterListing(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	alert := model.NewAlert(model.OrgID("org6"), "u1", "gone",
		model.WithAlertQuery(`{type="proxy.request"}`),
		model.WithAlertWindow(time.Hour),
		model.WithAlertEnabled(true),
	)
	if err := store.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	snapshot, err := store.ListEnabledAlerts(ctx)
	if err != nil {
		t.Fatalf("ListEnabledAlerts: %v", err)
	}
	listed := deletedOnReload{staleAlerts: staleAlerts{Store: store, snapshot: snapshot}}
	eventeval.NewEvaluator(listed, store, store, time.Nanosecond).EvaluateAllForTest(ctx)
}

type deletedOnReload struct{ staleAlerts }

func (deletedOnReload) GetAlertByID(context.Context, model.AlertID) (model.Alert, error) {
	return nil, port.ErrNotFound
}
