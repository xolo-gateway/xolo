package exchangerate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const defaultFrankfurterBaseURL = "https://api.frankfurter.app"

// FrankfurterProvider fetches exchange rates from api.frankfurter.app (ECB data, no API key required).
type FrankfurterProvider struct {
	baseURL string
	client  *http.Client
}

// fetchTimeout bounds a rate fetch: conversions run in requests, which a
// silent upstream must not hold.
const fetchTimeout = 10 * time.Second

func NewFrankfurterProvider() *FrankfurterProvider {
	return &FrankfurterProvider{
		baseURL: defaultFrankfurterBaseURL,
		client:  &http.Client{Timeout: fetchTimeout},
	}
}

type frankfurterResponse struct {
	Base  string             `json:"base"`
	Rates map[string]float64 `json:"rates"`
}

func (p *FrankfurterProvider) FetchRates(ctx context.Context, base string) (map[string]float64, error) {
	url := fmt.Sprintf("%s/latest?from=%s", p.baseURL, base)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("frankfurter: unexpected status %d", resp.StatusCode)
	}
	var payload frankfurterResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	// Add identity rate for the base currency itself
	payload.Rates[base] = 1.0
	return payload.Rates, nil
}

var _ interface {
	FetchRates(context.Context, string) (map[string]float64, error)
} = &FrankfurterProvider{}
