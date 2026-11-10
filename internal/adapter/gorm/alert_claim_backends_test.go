package gorm_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
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

func TestOpenIncidentOnEachBackend(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		alert := model.NewAlert(model.OrgID("org-open"), "u1", "opened",
			model.WithAlertQuery(`{type="proxy.request"}`),
			model.WithAlertWindow(time.Hour),
			model.WithAlertEnabled(true),
		)
		if err := store.CreateAlert(ctx, alert); err != nil {
			t.Fatalf("CreateAlert: %v", err)
		}

		// Instances firing the same crossing together: one incident.
		var opened atomic.Int32
		var wg sync.WaitGroup
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := store.OpenIncident(ctx, model.NewAlertIncident(alert.ID(), alert.OrgID(), 1))
				if err != nil {
					t.Errorf("open: %v", err)
				}
				if ok {
					opened.Add(1)
				}
			}()
		}
		wg.Wait()
		if got := opened.Load(); got != 1 {
			t.Fatalf("expected one incident opened, got %d", got)
		}

		// Once resolved, the next crossing opens a new one.
		open, err := store.GetOpenIncident(ctx, alert.ID())
		if err != nil {
			t.Fatalf("GetOpenIncident: %v", err)
		}
		if err := store.ResolveIncident(ctx, open.ID(), time.Now()); err != nil {
			t.Fatalf("ResolveIncident: %v", err)
		}
		if ok, err := store.OpenIncident(ctx, model.NewAlertIncident(alert.ID(), alert.OrgID(), 1)); err != nil || !ok {
			t.Fatalf("expected a new incident after resolution, got %v (%v)", ok, err)
		}
	})
}
