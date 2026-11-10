package gorm_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

func TestClaimAlertEvaluationOnEachBackend(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		alert := model.NewAlert(model.OrgID("org-claim"), "u1", "claimed",
			model.WithAlertQuery(`{type="proxy.request"}`),
			model.WithAlertWindow(time.Hour),
			model.WithAlertEnabled(true),
		)
		if err := store.CreateAlert(ctx, alert); err != nil {
			t.Fatalf("CreateAlert: %v", err)
		}

		// Instances racing for the same pass: exactly one gets it.
		now := time.Now()
		var claims atomic.Int32
		var wg sync.WaitGroup
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				claimed, err := store.ClaimAlertEvaluation(ctx, alert.ID(), now, 30*time.Second)
				if err != nil {
					t.Errorf("claim: %v", err)
				}
				if claimed {
					claims.Add(1)
				}
			}()
		}
		wg.Wait()
		if got := claims.Load(); got != 1 {
			t.Fatalf("expected one claim, got %d", got)
		}

		// Within minAge of the claimed pass: refused.
		if claimed, _ := store.ClaimAlertEvaluation(ctx, alert.ID(), now.Add(10*time.Second), 30*time.Second); claimed {
			t.Fatal("claimed again within minAge")
		}
		// The next period: granted.
		if claimed, _ := store.ClaimAlertEvaluation(ctx, alert.ID(), now.Add(31*time.Second), 30*time.Second); !claimed {
			t.Fatal("next period not claimed")
		}
	})
}

func TestTransitionAlertOnEachBackend(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		alert := model.NewAlert(model.OrgID("org-transition"), "u1", "transition",
			model.WithAlertQuery(`{type="proxy.request"}`),
			model.WithAlertWindow(time.Hour),
			model.WithAlertEnabled(true),
		)
		if err := store.CreateAlert(ctx, alert); err != nil {
			t.Fatalf("CreateAlert: %v", err)
		}
		open := func() int {
			t.Helper()
			incidents, err := store.ListIncidents(ctx, port.IncidentFilter{AlertID: ptr(alert.ID())})
			if err != nil {
				t.Fatalf("ListIncidents: %v", err)
			}
			n := 0
			for _, i := range incidents {
				if i.ResolvedAt() == nil {
					n++
				}
			}
			return n
		}

		first := time.Now()
		if ok, err := store.ClaimAlertEvaluation(ctx, alert.ID(), first, 0); err != nil || !ok {
			t.Fatalf("claim: %v (%v)", ok, err)
		}

		// Decisions sharing that claim, applied together: one wins.
		var applied atomic.Int32
		var wg sync.WaitGroup
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := store.TransitionAlert(ctx, port.AlertTransition{
					AlertID: alert.ID(), ClaimedAt: first, From: model.AlertStateOK, To: model.AlertStateFiring,
					Open: model.NewAlertIncident(alert.ID(), alert.OrgID(), 1),
				})
				if err != nil {
					t.Errorf("transition: %v", err)
				}
				if res.Applied {
					applied.Add(1)
				}
			}()
		}
		wg.Wait()
		if got := applied.Load(); got != 1 || open() != 1 {
			t.Fatalf("expected one applied transition and one open incident, got %d and %d", got, open())
		}

		// Another instance claims the next period; the first one's late
		// decision to resolve is dropped and leaves its incident alone.
		second := first.Add(time.Minute)
		if ok, _ := store.ClaimAlertEvaluation(ctx, alert.ID(), second, 0); !ok {
			t.Fatal("second claim refused")
		}
		resolveAt := time.Now()
		res, err := store.TransitionAlert(ctx, port.AlertTransition{
			AlertID: alert.ID(), ClaimedAt: first, From: model.AlertStateFiring, To: model.AlertStateOK, ResolveAt: &resolveAt,
		})
		if err != nil || res.Applied || open() != 1 {
			t.Fatalf("expected the stale transition dropped, got %+v (%v), %d open", res, err, open())
		}

		// The current claim raises the peak, without a second incident.
		peak := 5.0
		res, err = store.TransitionAlert(ctx, port.AlertTransition{
			AlertID: alert.ID(), ClaimedAt: second, From: model.AlertStateFiring, To: model.AlertStateFiring,
			Peak: &peak, Open: model.NewAlertIncident(alert.ID(), alert.OrgID(), 5),
		})
		if err != nil || !res.Applied || res.Opened != nil || open() != 1 {
			t.Fatalf("expected the peak raised on the open incident, got %+v (%v), %d open", res, err, open())
		}
		current, err := store.GetOpenIncident(ctx, alert.ID())
		if err != nil || current.PeakValue() != 5 {
			t.Fatalf("expected peak 5, got %v (%v)", current, err)
		}

		// And resolves it once below the threshold.
		third := second.Add(time.Minute)
		_, _ = store.ClaimAlertEvaluation(ctx, alert.ID(), third, 0)
		res, err = store.TransitionAlert(ctx, port.AlertTransition{
			AlertID: alert.ID(), ClaimedAt: third, From: model.AlertStateFiring, To: model.AlertStateOK, ResolveAt: &resolveAt,
		})
		if err != nil || !res.Applied || open() != 0 {
			t.Fatalf("expected the incident resolved, got %+v (%v), %d open", res, err, open())
		}
	})
}

