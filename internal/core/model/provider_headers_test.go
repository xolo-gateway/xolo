package model

import (
	"strings"
	"testing"
)

func TestProviderRequestHeaders_Valid(t *testing.T) {
	many := map[string]string{}
	for i := range maxProviderHeaders + 1 {
		many["X-Header-"+string(rune('A'+i))] = "v"
	}
	cases := map[string]struct {
		headers *ProviderRequestHeaders
		want    bool
	}{
		"none":             {nil, true},
		"opencode go":      {&ProviderRequestHeaders{Headers: map[string]string{"User-Agent": "xolo/1.0"}, SessionHeader: "x-opencode-session"}, true},
		"credentials":      {&ProviderRequestHeaders{Headers: map[string]string{"authorization": "Bearer x"}}, false},
		"api key session":  {&ProviderRequestHeaders{SessionHeader: "X-Api-Key"}, false},
		"framing":          {&ProviderRequestHeaders{Headers: map[string]string{"Content-Length": "1"}}, false},
		"invalid name":     {&ProviderRequestHeaders{Headers: map[string]string{"Bad Name": "v"}}, false},
		"injected newline": {&ProviderRequestHeaders{Headers: map[string]string{"X-A": "v\r\nX-B: w"}}, false},
		"value too long":   {&ProviderRequestHeaders{Headers: map[string]string{"X-A": strings.Repeat("v", maxProviderHeaderValue+1)}}, false},
		"too many headers": {&ProviderRequestHeaders{Headers: many}, false},
		"invalid session":  {&ProviderRequestHeaders{SessionHeader: "x session"}, false},
		"empty value":      {&ProviderRequestHeaders{Headers: map[string]string{"User-Agent": ""}}, false},
		"cookie":           {&ProviderRequestHeaders{Headers: map[string]string{"cookie": "s=1"}}, false},
		"forwarding":       {&ProviderRequestHeaders{Headers: map[string]string{"X-Forwarded-For": "10.0.0.1"}}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.headers.Valid(); got != tc.want {
				t.Errorf("Valid() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBaseProvider_SetRequestHeadersStoresAnEmptyConfigurationAsNone(t *testing.T) {
	p := NewProvider("org", "p", "openai", "", "", "USD")
	p.SetRequestHeaders(&ProviderRequestHeaders{Headers: map[string]string{}})
	if p.RequestHeaders() != nil {
		t.Errorf("RequestHeaders() = %+v, want nil", p.RequestHeaders())
	}
}
