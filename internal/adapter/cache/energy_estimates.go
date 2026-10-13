package cache

import (
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

type EnergyTotals struct {
	TotalEnergyWh    float64
	TotalCO2GramsMid float64
}

// EnergyEstimateCache memoizes the per-period energy/CO₂ totals. The estimate is
// non-linear per request, so SQL can only group the requests of the period by
// model and token counts: the query still reads every row of the range, and this
// cache keeps it off most page views. A few minutes of staleness on an
// approximate figure is an acceptable trade for that; the live cost charts are
// unaffected as they are aggregated in SQL on every request.
var EnergyEstimateCache = expirable.NewLRU[string, EnergyTotals](1000, nil, 5*time.Minute)
