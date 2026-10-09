package gorm

import (
	"encoding/json"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// encodeCatalogOverrides serialises the overrides for the overrides_json
// column. Overrides that declare nothing are stored as an empty string.
func encodeCatalogOverrides(o *model.CatalogOverrides) string {
	if o.IsZero() {
		return ""
	}
	data, err := json.Marshal(o)
	if err != nil {
		return ""
	}
	return string(data)
}

// decodeCatalogOverrides is the inverse of encodeCatalogOverrides. A value that
// does not parse reads as no overrides rather than failing the whole row.
func decodeCatalogOverrides(s string) *model.CatalogOverrides {
	if s == "" {
		return nil
	}
	var o model.CatalogOverrides
	if err := json.Unmarshal([]byte(s), &o); err != nil || o.IsZero() {
		return nil
	}
	return &o
}
