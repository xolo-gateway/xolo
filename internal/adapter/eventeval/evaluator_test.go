package eventeval_test

import (
	"context"
	"errors"
	"sync/atomic"
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

// Two instances listing the alerts at the same time both see it ok: the
// claim lets only the first one evaluate it this period. The second one would
// otherwise evaluate again (reloading the alert keeps it from firing twice,
// OpenIncident too, but every alert would be evaluated N times per period).
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

// countingEvents counts the evaluations reaching the event query.
type countingEvents struct {
	*xologorm.Store
	queries *atomic.Int32
}

func (s countingEvents) QueryEvents(ctx context.Context, filter port.EventFilter) ([]model.Event, error) {
	s.queries.Add(1)
	return s.Store.QueryEvents(ctx, filter)
}

// The claim itself: with a normal interval, two instances passing over the
// same stale listing evaluate the alert once.
func TestEvaluator_ClaimEvaluatesOncePerPeriod(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	alert := model.NewAlert(model.OrgID("org7"), "u1", "once",
		model.WithAlertQuery(`{type="proxy.request"}`),
		model.WithAlertWindow(time.Hour),
		model.WithAlertComparator(model.ComparatorGT),
		model.WithAlertThreshold(0),
		model.WithAlertEnabled(true),
	)
	if err := store.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	snapshot, err := store.ListEnabledAlerts(ctx)
	if err != nil {
		t.Fatalf("ListEnabledAlerts: %v", err)
	}
	listed := staleAlerts{Store: store, snapshot: snapshot}

	var queries atomic.Int32
	events := countingEvents{Store: store, queries: &queries}
	eventeval.NewEvaluator(listed, store, events, time.Minute).EvaluateAllForTest(ctx)
	eventeval.NewEvaluator(listed, store, events, time.Minute).EvaluateAllForTest(ctx)

	if got := queries.Load(); got != 1 {
		t.Fatalf("expected one evaluation in the period, got %d", got)
	}
}

// An incident left open on an alert that reads ok, by two overlapping
// evaluations deciding differently, is closed by the next evaluation below
// the threshold.
func TestEvaluator_ResolvesAnOrphanIncident(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	org := model.OrgID("org8")
	alert := model.NewAlert(org, "u1", "orphan",
		model.WithAlertQuery(`{type="proxy.request"}`),
		model.WithAlertWindow(time.Hour),
		model.WithAlertComparator(model.ComparatorGT),
		model.WithAlertThreshold(0),
		model.WithAlertEnabled(true),
	)
	if err := store.CreateAlert(ctx, alert); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	// What two diverging decisions could leave behind before transitions
	// were fenced: an open incident on an alert that reads ok.
	claimed := time.Now().Add(-time.Hour)
	if ok, err := store.ClaimAlertEvaluation(ctx, alert.ID(), claimed, 0); err != nil || !ok {
		t.Fatalf("claim: %v (%v)", ok, err)
	}
	if res, err := store.TransitionAlert(ctx, port.AlertTransition{
		AlertID: alert.ID(), ClaimedAt: claimed, From: model.AlertStateOK, To: model.AlertStateOK,
		Open: model.NewAlertIncident(alert.ID(), org, 1),
	}); err != nil || res.Opened == nil {
		t.Fatalf("open the orphan: %+v (%v)", res, err)
	}

	// No event: the condition does not hold, and the alert reads ok.
	eventeval.NewEvaluator(store, store, store, time.Nanosecond).EvaluateAllForTest(ctx)

	if _, err := store.GetOpenIncident(ctx, alert.ID()); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("expected the orphan incident resolved, got %v", err)
	}
}

// quietEvents holds the evaluation in its event query, then sees no event:
// that evaluation decides the alert is below its threshold.
type quietEvents struct {
	*xologorm.Store
	entered chan struct{}
	release chan struct{}
}

func (s quietEvents) QueryEvents(context.Context, port.EventFilter) ([]model.Event, error) {
	close(s.entered)
	<-s.release
	return nil, nil
}

// Two overlapping evaluations deciding differently: the slow one, below the
// threshold, is overtaken by a fast one that fires. The slow decision must be
// dropped whole: resolving the incident the fast one opened would leave a
// firing alert without an incident.
func TestEvaluator_StaleDecisionDoesNotResolveTheWinnersIncident(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	store := xologorm.NewStore(db)
	ctx := context.Background()
	org := model.OrgID("org9")

	alert := model.NewAlert(org, "u1", "diverging",
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

	quiet := quietEvents{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
	slow := eventeval.NewEvaluator(store, store, quiet, time.Nanosecond)
	fast := eventeval.NewEvaluator(store, store, store, time.Nanosecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		slow.EvaluateAllForTest(ctx)
	}()
	select {
	case <-quiet.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the slow evaluation never reached its event query")
	}
	fast.EvaluateAllForTest(ctx)
	close(quiet.release)
	<-done

	assertState(t, store, alert.ID(), model.AlertStateFiring)
	if _, err := store.GetOpenIncident(ctx, alert.ID()); err != nil {
		t.Fatalf("expected the fast evaluation's incident still open, got %v", err)
	}
}
