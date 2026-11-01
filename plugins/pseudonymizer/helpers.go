package main

import (
	"fmt"
	"strconv"
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
