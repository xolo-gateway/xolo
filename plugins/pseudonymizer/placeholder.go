package main

import (
	"regexp"
	"strings"
)

// placeholderPattern matches every placeholder go-anon writes — indexed
// ⟦PERSON_1_95881f⟧, hashed, or the ⟦…_REDACTED⟧ marker of a secret — whatever
// its nonce. It mirrors the unexported pattern go-anon uses to refuse a source
// text that already holds one (anonymizer.ErrPlaceholderCollision).
var placeholderPattern = regexp.MustCompile(`⟦[A-Za-z0-9_]{1,128}⟧`)

// eventPlaceholdersNeutralized reports placeholders rendered inert: invented
// by the model in a response, or found in the content of a request.
const eventPlaceholdersNeutralized = "placeholders.neutralized"

// redactedMarkerSuffix ends the marker of a redacted secret. No mapping ever
// restores it, by design.
const redactedMarkerSuffix = "_REDACTED⟧"

// neutralizePlaceholders rewrites the placeholders of text into an inert
// form, ⟦X⟧ becoming [[X]] as go-anon's own escaping does, and returns how
// many it rewrote. keep, when set, spares the placeholders it accepts.
//
// A neutralized placeholder is plain text: go-anon no longer refuses the text
// that carries it, and no restoration can substitute anything for it.
func neutralizePlaceholders(text string, keep func(placeholder string) bool) (string, int) {
	if !strings.Contains(text, "⟦") {
		return text, 0
	}
	count := 0
	out := placeholderPattern.ReplaceAllStringFunc(text, func(placeholder string) string {
		if keep != nil && keep(placeholder) {
			return placeholder
		}
		count++
		return "[[" + strings.TrimSuffix(strings.TrimPrefix(placeholder, "⟦"), "⟧") + "]]"
	})
	return out, count
}

// isRedactedMarker reports whether placeholder marks a redacted secret.
func isRedactedMarker(placeholder string) bool {
	return strings.HasSuffix(placeholder, redactedMarkerSuffix)
}
