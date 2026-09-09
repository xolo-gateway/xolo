package promptguard

import "strings"

// Quotation marks that open and close a quoted span. Straight double quotes
// pair up in order; apostrophes are not considered, they are too common as
// elision marks in French.
var quotePairs = map[rune]rune{'«': '»', '“': '”', '„': '“', '‹': '›', '"': '"'}

// quotedSpans returns the byte ranges of the text enclosed in quotation
// marks. An unclosed quote runs to the end of the text.
func quotedSpans(s string) [][2]int {
	var spans [][2]int
	var closer rune
	start := -1
	for i, r := range s {
		if start < 0 {
			if c, ok := quotePairs[r]; ok {
				closer = c
				start = i + len(string(r))
			}
			continue
		}
		if r == closer {
			spans = append(spans, [2]int{start, i})
			start = -1
		}
	}
	if start >= 0 {
		spans = append(spans, [2]int{start, len(s)})
	}
	return spans
}

// frameCues are the words that, before a quote, say the quoted text is
// being talked about rather than obeyed. Matched on the canonical form.
var frameCues = []string{
	"translat", "traduis", "traduc", "explain", "expli", "classif", "classe ", "categor", "catégor",
	"test", "example", "exemple", "why", "pourquoi", "context", "contexte", "correct", "corrig",
	"rephrase", "reformul", "analy", "detect", "détect", "filter", "filtre", "quote", "citation",
	"cite", "is this", "est-ce", "sentence", "phrase", "spelling", "orthograph", "grammar", "grammaire",
	"documentation", "malicious", "malveillant", "attack", "attaque", "injection", "legitimate", "légitime",
	"review", "relis", "summar", "résum", "what does", "que signifie", "que veut dire", "mean",
	"proofread", "audit", "dataset", "corpus", "label", "étiquet", "wrote", "écrirait", "écrit",
}

// allQuoted reports whether every visible match lies inside a quoted span
// whose preamble carries a framing cue. Matches found in decoded or unfolded
// text are never considered quoted: their offsets do not refer to the
// canonical text, and hiding an attack in Base64 inside quotes is not
// what an honest translation request looks like.
func allQuoted(canonical string, matches []Match) bool {
	spans := quotedSpans(canonical)
	if len(spans) == 0 {
		return false
	}
	first := spans[0][0]
	preamble := canonical[:first]
	if len(strings.TrimSpace(preamble)) < 4 || !hasFrameCue(preamble) {
		return false
	}
	for _, m := range matches {
		if m.Decoded {
			return false
		}
		// A match is quoted when it ends inside a span. Its start may sit
		// before the opening quote: the regexps are leftmost, so "write ...
		// your system prompt" begins on the framing verb itself.
		inside := false
		for _, sp := range spans {
			if m.End > sp[0] && m.End <= sp[1] {
				inside = true
				break
			}
		}
		if !inside {
			return false
		}
	}
	return true
}

func hasFrameCue(preamble string) bool {
	for _, c := range frameCues {
		if strings.Contains(preamble, c) {
			return true
		}
	}
	return false
}
