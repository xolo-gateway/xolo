package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/a-h/templ"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// formatFloat formats a float64 as a string for HTML input fields.
// Returns empty string for zero to let the placeholder show.
func formatFloat(f float64) string {
	if f == 0 {
		return ""
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// formatBias formats the bias for its input field, 0 included: unlike the
// thresholds, 0 is a setting of its own, not "no limit".
func formatBias(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// languageNames maps ISO 639-1 codes to their display name in the UI.
var languageNames = map[string]string{
	"fr": "Français",
	"en": "English",
	"es": "Español",
}

// languageLabel returns the display label of an ISO 639-1 language code.
func languageLabel(code string) string {
	if name, ok := languageNames[code]; ok {
		return fmt.Sprintf("%s (%s)", name, code)
	}
	return code
}

// numberAttributes returns the attributes of a number input: never negative,
// with an optional step and maximum.
func numberAttributes(step, max string) templ.Attributes {
	attrs := templ.Attributes{"min": "0"}
	if step != "" {
		attrs["step"] = step
	}
	if max != "" {
		attrs["max"] = max
	}
	return attrs
}

// formatInt formats an int as a string for HTML input fields.
// Returns empty string for zero to let the placeholder show.
func formatInt(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

// hashKeyPlaceholder returns the input placeholder for the HMAC key field,
// showing dots when a key is already configured.
func hashKeyPlaceholder(hasValue bool) string {
	if hasValue {
		return "••••••••"
	}
	return ""
}

// typeGroupTitle returns the title of a family of types on the page.
func typeGroupTitle(ctx context.Context, group string) string {
	switch group {
	case groupIdentifiers:
		return i18n.T(ctx, "plugin.pseudonymizer.types.group.identifiers")
	case groupSecrets:
		return i18n.T(ctx, "plugin.pseudonymizer.types.group.secrets")
	}
	return i18n.T(ctx, "plugin.pseudonymizer.types.group.content")
}
