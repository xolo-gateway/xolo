package gorm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	gormpkg "gorm.io/gorm"
)

func TestMigrateCreatesExchangeRatesTable(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		store := newStoreOn(t, db)
		if db.Migrator().HasTable(&xologorm.ExchangeRate{}) {
			t.Fatal("exchange_rates table exists before migration")
		}

		if err := store.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		if !db.Migrator().HasTable(&xologorm.ExchangeRate{}) {
			t.Fatal("exchange_rates table does not exist after migration")
		}
	})
}

func TestExchangeRateStore(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := context.Background()
		firstFetch := time.Date(2026, time.September, 18, 8, 0, 0, 0, time.UTC)
		updatedFetch := firstFetch.Add(time.Hour)

		// The first operation on a raw NewStore must retain the lazy migration
		// behaviour used by callers outside application setup.
		if err := store.UpsertRate(ctx, model.ExchangeRate{
			FromCurrency: "EUR",
			ToCurrency:   "USD",
			Rate:         1.18,
			FetchedAt:    firstFetch,
		}); err != nil {
			t.Fatalf("insert rate: %v", err)
		}

		got, err := store.GetRate(ctx, "EUR", "USD")
		if err != nil {
			t.Fatalf("get inserted rate: %v", err)
		}
		assertExchangeRate(t, got, model.ExchangeRate{
			FromCurrency: "EUR",
			ToCurrency:   "USD",
			Rate:         1.18,
			FetchedAt:    firstFetch,
		})

		if err := store.UpsertRate(ctx, model.ExchangeRate{
			FromCurrency: "EUR",
			ToCurrency:   "USD",
			Rate:         1.19,
			FetchedAt:    updatedFetch,
		}); err != nil {
			t.Fatalf("update rate: %v", err)
		}
		if err := store.UpsertRate(ctx, model.ExchangeRate{
			FromCurrency: "EUR",
			ToCurrency:   "GBP",
			Rate:         0.87,
			FetchedAt:    updatedFetch,
		}); err != nil {
			t.Fatalf("insert second rate: %v", err)
		}

		rates, err := store.ListRates(ctx)
		if err != nil {
			t.Fatalf("list rates: %v", err)
		}
		if len(rates) != 2 {
			t.Fatalf("listed %d rates, want 2", len(rates))
		}
		assertExchangeRate(t, &rates[0], model.ExchangeRate{
			FromCurrency: "EUR",
			ToCurrency:   "GBP",
			Rate:         0.87,
			FetchedAt:    updatedFetch,
		})
		assertExchangeRate(t, &rates[1], model.ExchangeRate{
			FromCurrency: "EUR",
			ToCurrency:   "USD",
			Rate:         1.19,
			FetchedAt:    updatedFetch,
		})

		if _, err := store.GetRate(ctx, "USD", "JPY"); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("get missing rate: got %v, want %v", err, port.ErrNotFound)
		}
	})
}

func assertExchangeRate(t *testing.T, got *model.ExchangeRate, want model.ExchangeRate) {
	t.Helper()
	if got.FromCurrency != want.FromCurrency ||
		got.ToCurrency != want.ToCurrency ||
		got.Rate != want.Rate ||
		!got.FetchedAt.Equal(want.FetchedAt) {
		t.Errorf("rate: got %+v, want %+v", *got, want)
	}
}
