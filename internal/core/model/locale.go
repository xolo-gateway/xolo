package model

import (
	"context"
	"regexp"
	"strings"
)

// MaxLocaleLength is the length in bytes of the longest member locale.
const MaxLocaleLength = 12

// localePattern is a canonical language tag of the common contract: a
// language, then optionally a script, then optionally a region. Case is
// significant and never rewritten.
var localePattern = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z][a-z]{3})?(-([A-Z]{2}|[0-9]{3}))?$`)

// NormalizeLocale trims the locale of a member, the only normalization the
// common contract applies to it.
func NormalizeLocale(locale string) string { return strings.TrimSpace(locale) }

// IsValidLocale reports whether a normalized locale may be stored: empty,
// which clears the choice, or a canonical language tag. A valid tag Xolo
// does not offer is kept: the interface serves the closest language.
func IsValidLocale(locale string) bool {
	return locale == "" || (len(locale) <= MaxLocaleLength && localePattern.MatchString(locale))
}

type localeChangeKey struct{}

// WithLocaleChange designates the member whose own language choice a
// transaction writes. It may write that member's locale, and nothing else,
// even when members belong to the control plane.
func WithLocaleChange(ctx context.Context, id UserID) context.Context {
	return context.WithValue(ctx, localeChangeKey{}, id)
}

// LocaleChange returns the member designated by WithLocaleChange.
func LocaleChange(ctx context.Context) (UserID, bool) {
	id, ok := ctx.Value(localeChangeKey{}).(UserID)
	return id, ok && id != ""
}
