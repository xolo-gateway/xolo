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

// ownedBy returns a predicate matching the indexed placeholders a session with
// nonce writes: ⟦PERSON_1_<nonce>⟧. Hashed placeholders and the markers of
// redacted secrets carry no nonce, so no node can tell its own from another's.
// The index is required: without a key, a hashed placeholder ends with six
// hexadecimal digits, the very shape of a nonce.
//
// A pipeline can hold several pseudonymizer nodes: a middleware wrapping a
// virtual model that has its own. Each node derives its own nonce, so the
// placeholders of the other nodes are the ones this predicate rejects.
func ownedBy(nonce string) func(placeholder string) bool {
	if nonce == "" {
		return func(string) bool { return false }
	}
	indexed := regexp.MustCompile(`^⟦[A-Za-z0-9_]+_[0-9]+_` + regexp.QuoteMeta(nonce) + `⟧$`)
	return indexed.MatchString
}

// mentionsNonce reports whether text shows the model a placeholder of the
// node, live or inert. A model that saw none has no form to imitate.
//
// It is looser than ownedBy on purpose: any form ending with the nonce counts,
// including a hashed placeholder whose digest happens to end with it. A false
// positive only costs the streaming of one response, held back for
// PostResponse; a false negative would let an invented placeholder through.
func mentionsNonce(text, nonce string) bool {
	return nonce != "" && (strings.Contains(text, "_"+nonce+"⟧") || strings.Contains(text, "_"+nonce+"]]"))
}

// inert is the form a neutralized placeholder takes, ⟦X⟧ becoming [[X]] as
// go-anon's own escaping does.
func inert(placeholder string) string {
	return "[[" + strings.TrimSuffix(strings.TrimPrefix(placeholder, "⟦"), "⟧") + "]]"
}

// neutralizePlaceholders rewrites the placeholders of text that match into
// their inert form, and returns how many it rewrote. An inert placeholder is
// plain text: go-anon no longer refuses the text that carries it, and no
// restoration can substitute anything for it.
func neutralizePlaceholders(text string, match func(placeholder string) bool) (string, int) {
	if !strings.Contains(text, "⟦") {
		return text, 0
	}
	count := 0
	out := placeholderPattern.ReplaceAllStringFunc(text, func(placeholder string) string {
		if !match(placeholder) {
			return placeholder
		}
		count++
		return inert(placeholder)
	})
	return out, count
}

// maskedText is a text ready for go-anon: none of the placeholders it held is
// left in it.
type maskedText struct {
	// Text is what goes to the anonymizer.
	Text string
	// Neutralized counts the placeholders of this node, made inert for good.
	Neutralized int
	// foreign are the placeholders of other nodes, masked the same way and
	// put back by Unmask.
	foreign []string
}

// maskPlaceholders prepares text for go-anon, which refuses a text that
// already holds a placeholder. Those of this node are neutralized: the client
// only ever sees restored text, so one found here is a placeholder the model
// invented on an earlier turn, or an attempt to have one restored. Those of
// another node, upstream in the pipeline, are masked and put back after
// anonymization, for that node to restore them.
func maskPlaceholders(text string, own func(placeholder string) bool) maskedText {
	masked := maskedText{Text: text}
	if !strings.Contains(text, "⟦") {
		return masked
	}
	masked.Text = placeholderPattern.ReplaceAllStringFunc(text, func(placeholder string) string {
		if own(placeholder) {
			masked.Neutralized++
		} else {
			masked.foreign = append(masked.foreign, placeholder)
		}
		return inert(placeholder)
	})
	return masked
}

// Unmask puts the placeholders of other nodes back into anonymized, the text
// go-anon returned for m.Text, and returns how many it could not find. It
// relies on the anonymizer leaving an inert form alone: one it rewrote is
// lost for the node that owns it, which then cannot restore it.
func (m maskedText) Unmask(anonymized string) (string, int) {
	missing := 0
	for _, placeholder := range m.foreign {
		form := inert(placeholder)
		if !strings.Contains(anonymized, form) {
			missing++
			continue
		}
		anonymized = strings.Replace(anonymized, form, placeholder, 1)
	}
	return anonymized, missing
}
