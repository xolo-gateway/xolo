package gorm

import (
	"encoding/json"
	"log/slog"

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
		// Unreachable while the overrides hold plain numbers and booleans.
		slog.Error("could not encode catalog overrides, they are not saved", slog.Any("error", err))
		return ""
	}
	return string(data)
}

// decodeCatalogOverrides is the inverse of encodeCatalogOverrides. A value that
// does not parse reads as no overrides rather than failing the whole row, and
// is logged so the fallback to derivation can be traced.
func decodeCatalogOverrides(s string) *model.CatalogOverrides {
	if s == "" {
		return nil
	}
	var o model.CatalogOverrides
	if err := json.Unmarshal([]byte(s), &o); err != nil {
		slog.Warn("ignoring unreadable catalog overrides", slog.Any("error", err))
		return nil
	}
	if o.IsZero() {
		return nil
	}
	return &o
}
