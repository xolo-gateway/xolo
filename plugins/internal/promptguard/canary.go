package promptguard

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Canaries are strings the operator plants in a system prompt to measure
// leakage, or genuinely sensitive values that must never reach a client. The
// response inspection looks for them in the model's answer, in clear and
// under the cheap disguises a model produces when asked to "not reveal it
// but...": letters spaced or hyphenated, case changed, invisible characters
// inserted, homoglyphs, the string reversed, ROT13, Base64, hexadecimal, or
// a long contiguous fragment of it. It cannot see a value leaked one bit at
// a time through yes/no answers: that channel is only closed by keeping the
// value out of the model's context.

const (
	// FindingCanary is a canary found whole, in clear or transformed.
	FindingCanary ResponseFindingKind = "canary"
	// FindingCanaryFragment is a long contiguous piece of a canary.
	FindingCanaryFragment ResponseFindingKind = "canary_fragment"

	// CanaryWeight is near certainty: an honest answer never contains it.
	CanaryWeight = 0.95
	// CanaryFragmentWeight leaves room for a coincidence on short values.
	CanaryFragmentWeight = 0.7

	// MinCanaryLength is the squeezed length under which a canary is ignored,
	// with a warning from the caller: "abc" would flag every answer.
	MinCanaryLength = 4
	// minFragmentLength bounds the fragment window from below.
	minFragmentLength = 6
)

// CanaryHit tells which canary leaked and how. The value never appears.
type CanaryHit struct {
	// Index is the 1-based position of the canary in the configured list.
	Index int `json:"index"`
	// Form is how it appeared: exact, reversed, rot13, base64, hex, fragment.
	Form string `json:"form"`
}

// squeezed is a text reduced to its lower-cased letters and digits, with
// homoglyphs folded, plus the byte span each squeezed byte comes from. It
// makes "M-R-D 4 4 1 7", "mrd4417" and "ＭＲＤ4417" the same string while
// keeping enough to redact the original.
type squeezed struct {
	text  string
	start []int // start[i]: byte offset in the original of squeezed byte i
	end   []int // end[i]: byte offset just after that original rune
}

func squeeze(s string) squeezed {
	var b strings.Builder
	sq := squeezed{}
	b.Grow(len(s))
	for i, orig := range s {
		size := len(string(orig))
		// NFKC rune by rune: fullwidth forms and mathematical alphanumerics
		// fold to ASCII, a ligature expands to its letters, and every byte
		// written keeps pointing at the original rune.
		for _, r := range norm.NFKC.String(string(orig)) {
			if latin, ok := homoglyphs[r]; ok {
				r = latin
			}
			r = unicode.ToLower(r)
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				continue
			}
			from := b.Len()
			b.WriteRune(r)
			for k := from; k < b.Len(); k++ {
				sq.start = append(sq.start, i)
				sq.end = append(sq.end, i+size)
			}
		}
	}
	sq.text = b.String()
	return sq
}

// spans returns the original byte spans of every non-overlapping occurrence
// of needle in the squeezed text.
func (sq squeezed) spans(needle string) [][2]int {
	if needle == "" {
		return nil
	}
	var out [][2]int
	from := 0
	for {
		i := strings.Index(sq.text[from:], needle)
		if i < 0 {
			return out
		}
		i += from
		j := i + len(needle)
		out = append(out, [2]int{sq.start[i], sq.end[j-1]})
		from = j
	}
}

// canaryForms lists the disguises checked for one canary, squeezed, keyed by
// the form name reported in the hit.
func canaryForms(canary string) map[string]string {
	forms := map[string]string{
		"exact":    canary,
		"reversed": reverseRunes(canary),
		"rot13":    rot13(canary),
		"base64":   base64.StdEncoding.EncodeToString([]byte(canary)),
		"hex":      hex.EncodeToString([]byte(canary)),
	}
	out := make(map[string]string, len(forms))
	for name, f := range forms {
		if sq := squeeze(f).text; len(sq) >= MinCanaryLength {
			out[name] = sq
		}
	}
	return out
}

func reverseRunes(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func rot13(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return 'a' + (r-'a'+13)%26
		case r >= 'A' && r <= 'Z':
			return 'A' + (r-'A'+13)%26
		}
		return r
	}, s)
}

// findCanaries scans text for the configured canaries and returns the
// findings with their spans and the hits. Canaries too short to be
// meaningful are skipped; ValidCanaries tells the caller which ones.
func findCanaries(text string, canaries []string) ([]ResponseFinding, []CanaryHit) {
	if len(canaries) == 0 {
		return nil, nil
	}
	sq := squeeze(text)
	if sq.text == "" {
		return nil, nil
	}
	var findings []ResponseFinding
	var hits []CanaryHit
	for idx, c := range canaries {
		c = strings.TrimSpace(c)
		forms := canaryForms(c)
		if len(forms) == 0 {
			continue
		}
		found := false
		for _, name := range []string{"exact", "reversed", "rot13", "base64", "hex"} {
			f, ok := forms[name]
			if !ok {
				continue
			}
			spans := sq.spans(f)
			if len(spans) == 0 {
				continue
			}
			found = true
			hits = append(hits, CanaryHit{Index: idx + 1, Form: name})
			for _, sp := range spans {
				end := sp[1]
				if name == "base64" {
					// The padding is not a letter and fell out of the
					// squeezed text; take it along.
					for end < len(text) && text[end] == '=' {
						end++
					}
				}
				findings = append(findings, ResponseFinding{Kind: FindingCanary, Weight: CanaryWeight, Start: sp[0], End: end})
			}
		}
		if found {
			continue
		}
		// No whole form: look for a long contiguous piece of the clear value.
		// The window is half the value, never under six characters, so a
		// sixteen-character token leaks as soon as eight consecutive
		// characters do.
		exact := forms["exact"]
		window := len(exact) / 2
		if window < minFragmentLength {
			window = minFragmentLength
		}
		if window >= len(exact) {
			continue
		}
		var spans [][2]int
		for i := 0; i+window <= len(exact); i++ {
			if s := sq.spans(exact[i : i+window]); len(s) > 0 {
				spans = append(spans, s...)
			}
		}
		if len(spans) == 0 {
			continue
		}
		hits = append(hits, CanaryHit{Index: idx + 1, Form: "fragment"})
		for _, sp := range mergeSpans(spans) {
			findings = append(findings, ResponseFinding{Kind: FindingCanaryFragment, Weight: CanaryFragmentWeight, Start: sp[0], End: sp[1]})
		}
	}
	return findings, hits
}

// ValidCanaries returns the canaries long enough to be checked, trimmed, and
// the 1-based indices of the ones that were dropped. The indices let a
// caller keep the configured numbering in its events.
func ValidCanaries(canaries []string) (kept []string, dropped []int) {
	for i, c := range canaries {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if len(squeeze(c).text) < MinCanaryLength {
			dropped = append(dropped, i+1)
			continue
		}
		kept = append(kept, c)
	}
	return kept, dropped
}

// mergeSpans unions overlapping or adjacent spans, sorted by start.
func mergeSpans(spans [][2]int) [][2]int {
	if len(spans) < 2 {
		return spans
	}
	sortSpans(spans)
	out := [][2]int{spans[0]}
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		if s[0] <= last[1] {
			if s[1] > last[1] {
				last[1] = s[1]
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

func sortSpans(spans [][2]int) {
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j][0] < spans[j-1][0]; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
}
