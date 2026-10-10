package component

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// ErrInvalidCatalogOverrides reports a token count that is not a positive
// integer. A blank field is not an error: it leaves derivation in charge.
var ErrInvalidCatalogOverrides = errors.New("invalid catalog overrides")

// ParseCatalogOverrides reads the fields CatalogOverridesFields renders. It
// returns nil when the form declares nothing, so a model left alone keeps
// being derived.
func ParseCatalogOverrides(r *http.Request) (*model.CatalogOverrides, error) {
	contextWindow, err := parseTokenCount(r.FormValue("catalog_context_window"))
	if err != nil {
		return nil, err
	}
	maxOutput, err := parseTokenCount(r.FormValue("catalog_max_output_tokens"))
	if err != nil {
		return nil, err
	}
	o := &model.CatalogOverrides{ContextWindow: contextWindow, MaxOutputTokens: maxOutput}
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
		return nil, nil
	}
	return o, nil
}

func parseTokenCount(v string) (int64, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.Wrapf(ErrInvalidCatalogOverrides, "%q is not a positive integer", v)
	}
	return n, nil
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
