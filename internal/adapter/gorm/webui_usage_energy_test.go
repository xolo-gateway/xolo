package gorm_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/adapter/cache"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui"
	"github.com/xolo-gateway/xolo/internal/http/middleware/memberships"
)

// countingUsageStore counts the energy aggregations reaching the store.
type countingUsageStore struct {
	port.UsageStore
	sizes atomic.Int32
}

func (s *countingUsageStore) AggregateRequestSizes(ctx context.Context, filter port.UsageFilter) ([]port.RequestSize, error) {
	s.sizes.Add(1)
	return s.UsageStore.AggregateRequestSizes(ctx, filter)
}

// The energy estimate is cached by range: a page loaded again a moment later
// does not aggregate the period a second time.
func TestUsagePagesReuseTheEnergyEstimate(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		cache.EnergyEstimateCache.Purge()
		f := newTeamPagesFixture(t, store)
		usage := &countingUsageStore{UsageStore: store}
		f.handler = memberships.Middleware(store, store)(webui.NewHandler(
			nil, store, store, store, store, store, store, store, usage, store,
			service.NewInvitationService(store), store, store, service.NewQuotaService(store, store),
			service.NewExchangeRateService(nil, store, 0), store, scopeTestSecretKey,
			nil, nil, nil, store, store, store, store, 100, 100,
		).WithTeams(store))

		ctx := context.Background()
		provider := model.NewProvider(f.org.ID(), "Local", "openai", "", "k", "EUR")
		require.NoError(t, store.CreateProvider(ctx, provider))
		llm := model.NewLLMModel(provider.ID(), f.org.ID(), "gpt", "gpt", "", 0, 0)
		llm.SetActiveParams(8_000_000_000)
		require.NoError(t, store.CreateLLMModel(ctx, llm))
		rec := model.NewUsageRecord(f.owner.ID(), "", f.org.ID(), "", provider.ID(), llm.ID(), "gpt", "", 10, 0, 5, 0, "EUR", model.CostSourceProvider, "")
		require.NoError(t, store.RecordUsage(ctx, rec))

		// The second pass comes over a second later: the start of the range has
		// moved, the cached estimate must still answer.
		paths := []string{"/usage?range=30d", "/orgs/acme/usage?range=30d"}
		for pass := range 2 {
			if pass > 0 {
				time.Sleep(1100 * time.Millisecond)
			}
			for _, path := range paths {
				rr := f.do(t, f.owner, http.MethodGet, path, nil)
				require.Equal(t, http.StatusOK, rr.Code, path)
			}
		}
		require.Equal(t, int32(len(paths)), usage.sizes.Load())
		// The cached figures come from the recorded request, not an empty period.
		cached := cache.EnergyEstimateCache.Values()
		require.Len(t, cached, len(paths))
		for _, totals := range cached {
			require.Positive(t, totals.TotalEnergyWh)
			require.Positive(t, totals.TotalCO2GramsMid)
		}

		// Spellings of one period, or one set of owners in another order or
		// repeated, share the estimate.
		owner, lead := string(f.owner.ID()), string(f.lead.ID())
		for _, same := range [][]string{
			{"/usage", "/usage?range=7d", "/usage?range=unknown"},
			{"/orgs/acme/usage?owner=" + owner + "&owner=" + lead, "/orgs/acme/usage?owner=" + lead + "," + owner, "/orgs/acme/usage?owner=" + owner + "&owner=" + lead + "," + owner},
		} {
			before := usage.sizes.Load()
			for _, path := range same {
				rr := f.do(t, f.owner, http.MethodGet, path, nil)
				require.Equal(t, http.StatusOK, rr.Code, path)
			}
			require.Equal(t, int32(1), usage.sizes.Load()-before, same)
		}
	})
}
