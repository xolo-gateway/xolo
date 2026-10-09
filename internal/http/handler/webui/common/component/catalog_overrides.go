package component

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// ParseCatalogOverrides reads the fields CatalogOverridesFields renders. It
// returns nil when the form declares nothing, so a model left alone keeps
// being derived.
func ParseCatalogOverrides(r *http.Request) *model.CatalogOverrides {
	o := &model.CatalogOverrides{
		ContextWindow:   parseTokenCount(r.FormValue("catalog_context_window")),
		MaxOutputTokens: parseTokenCount(r.FormValue("catalog_max_output_tokens")),
	}
	if r.FormValue("catalog_caps_declared") == "on" {
		o.Capabilities = &model.ModelCapabilities{
			Tools:      r.FormValue("catalog_cap_tools") == "on",
			Vision:     r.FormValue("catalog_cap_vision") == "on",
			Reasoning:  r.FormValue("catalog_cap_reasoning") == "on",
			Audio:      r.FormValue("catalog_cap_audio") == "on",
			Embeddings: r.FormValue("catalog_cap_embeddings") == "on",
		}
	}
	if o.IsZero() {
		return nil
	}
	return o
}

func parseTokenCount(v string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func overrideTokens(o *model.CatalogOverrides, pick func(*model.CatalogOverrides) int64) string {
	if o == nil {
		return ""
	}
	if v := pick(o); v > 0 {
		return fmt.Sprintf("%d", v)
	}
	return ""
}

func overrideContextWindow(o *model.CatalogOverrides) string {
	return overrideTokens(o, func(o *model.CatalogOverrides) int64 { return o.ContextWindow })
}

func overrideMaxOutput(o *model.CatalogOverrides) string {
	return overrideTokens(o, func(o *model.CatalogOverrides) int64 { return o.MaxOutputTokens })
}

// overrideCaps returns the declared capabilities, or the zero value when none
// are declared.
func overrideCaps(o *model.CatalogOverrides) model.ModelCapabilities {
	if o == nil || o.Capabilities == nil {
		return model.ModelCapabilities{}
	}
	return *o.Capabilities
}

func overrideCapsDeclared(o *model.CatalogOverrides) bool {
	return o != nil && o.Capabilities != nil
}
