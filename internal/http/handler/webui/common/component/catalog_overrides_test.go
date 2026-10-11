package component

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pkg/errors"
)

func formRequest(values url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestParseCatalogOverrides(t *testing.T) {
	t.Run("an empty form declares nothing", func(t *testing.T) {
		o, err := ParseCatalogOverrides(formRequest(url.Values{}))
		if err != nil || o != nil {
			t.Fatalf("got %+v, %v; want nil, nil", o, err)
		}
	})

	t.Run("counts and capabilities", func(t *testing.T) {
		o, err := ParseCatalogOverrides(formRequest(url.Values{
			"catalog_context_window":    {"64000"},
			"catalog_max_output_tokens": {"4096"},
			"catalog_caps_declared":     {"on"},
			"catalog_cap_tools":         {"on"},
			"catalog_cap_vision":        {"on"},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if o.ContextWindow != 64_000 || o.MaxOutputTokens != 4_096 {
			t.Errorf("counts = %+v", o)
		}
		if o.Capabilities == nil || !o.Capabilities.Tools || !o.Capabilities.Vision || o.Capabilities.Reasoning {
			t.Errorf("capabilities = %+v", o.Capabilities)
		}
	})

	t.Run("declared with nothing ticked says no capabilities", func(t *testing.T) {
		o, err := ParseCatalogOverrides(formRequest(url.Values{"catalog_caps_declared": {"on"}}))
		if err != nil || o == nil || o.Capabilities == nil {
			t.Fatalf("got %+v, %v; want a declaration of no capabilities", o, err)
		}
	})

	t.Run("capabilities not declared are ignored", func(t *testing.T) {
		o, err := ParseCatalogOverrides(formRequest(url.Values{"catalog_cap_tools": {"on"}}))
		if err != nil || o != nil {
			t.Fatalf("got %+v, %v; want nil, nil", o, err)
		}
	})

	for _, bad := range []string{"-1", "abc", "1.5", "99999999999999999999"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			_, err := ParseCatalogOverrides(formRequest(url.Values{"catalog_context_window": {bad}}))
			if !errors.Is(err, ErrInvalidCatalogOverrides) {
				t.Fatalf("err = %v, want ErrInvalidCatalogOverrides", err)
			}
		})
	}
}
