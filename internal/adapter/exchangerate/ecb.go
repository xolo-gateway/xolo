package exchangerate

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
)

const defaultECBURL = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"

// ECBProvider fetches exchange rates directly from the European Central Bank
// daily reference rates XML feed. All published rates use EUR as the base
// currency; cross-rates for other bases are computed from the EUR rates.
type ECBProvider struct {
	url    string
	client *http.Client
}

func NewECBProvider() *ECBProvider {
	return &ECBProvider{
		url:    defaultECBURL,
		client: &http.Client{Timeout: fetchTimeout},
	}
}

// ecbEnvelope mirrors the ECB eurofxref XML structure.
type ecbEnvelope struct {
	XMLName xml.Name `xml:"Envelope"`
	Cube    struct {
		Cube struct {
			Time  string `xml:"time,attr"`
			Cubes []struct {
				Currency string  `xml:"currency,attr"`
				Rate     float64 `xml:"rate,attr"`
			} `xml:"Cube"`
		} `xml:"Cube"`
	} `xml:"Cube"`
}

// FetchRates returns exchange rates from base to all currencies available in
// the ECB feed. If base is "EUR" the rates are returned directly. For any
// other base currency, cross-rates are derived: rate(base→X) = rate(EUR→X) / rate(EUR→base).
func (p *ECBProvider) FetchRates(ctx context.Context, base string) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ecb: unexpected status %d", resp.StatusCode)
	}

	var envelope ecbEnvelope
	if err := xml.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("ecb: failed to decode XML: %w", err)
	}

	// Build EUR-based rate map.
	eurRates := make(map[string]float64, len(envelope.Cube.Cube.Cubes)+1)
	eurRates["EUR"] = 1.0
	for _, c := range envelope.Cube.Cube.Cubes {
		if c.Currency != "" && c.Rate > 0 {
			eurRates[c.Currency] = c.Rate
		}
	}

	if base == "EUR" {
		return eurRates, nil
	}

	// Cross-rate: base→X = EUR→X / EUR→base
	baseRate, ok := eurRates[base]
	if !ok {
		return nil, fmt.Errorf("ecb: base currency %q not found in ECB rates", base)
	}

	rates := make(map[string]float64, len(eurRates))
	for currency, eurRate := range eurRates {
		rates[currency] = eurRate / baseRate
	}
	return rates, nil
}
