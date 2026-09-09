// Package promptguard scores a text for prompt injection attempts without
// calling any model. It normalises the text, extracts structural signals
// (invisible characters, encoded payloads, role markers), matches a set of
// lexical rules and combines everything into an explainable risk in [0, 1].
//
// The package never decides what to do with the risk. The plugin built on top
// of it exposes the scores as pipeline ports and lets the pipeline decide.
package promptguard

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// DefaultMaxRunes bounds the amount of text analysed. Anything beyond is cut
// before normalisation so that every later step runs in bounded time.
const DefaultMaxRunes = 20000

// Normalized holds the representations of a text the analysers work on. The
// original text is kept untouched: what is sent to the model is never the
// canonical form.
type Normalized struct {
	// Original is the input, made valid UTF-8 and truncated to the rune limit.
	Original string
	// Canonical is the form the rules match against: NFKC, homoglyphs folded
	// to Latin, invisible characters removed, lower-cased, whitespace
	// collapsed.
	Canonical string
	RuneCount int
	ByteCount int
	Truncated bool

	// InvisibleCount counts zero-width, bidi-control and tag characters.
	InvisibleCount int
	// ControlCount counts C0/C1 control characters other than tab and newlines.
	ControlCount int
	// HomoglyphCount counts non-Latin letters folded to a Latin look-alike.
	HomoglyphCount int
}

// Normalize prepares a text for analysis. maxRunes <= 0 means DefaultMaxRunes.
func Normalize(text string, maxRunes int) Normalized {
	if maxRunes <= 0 {
		maxRunes = DefaultMaxRunes
	}
	text = strings.ToValidUTF8(text, "�")

	n := Normalized{}
	if count := utf8.RuneCountInString(text); count > maxRunes {
		text = truncateRunes(text, maxRunes)
		n.Truncated = true
		n.RuneCount = maxRunes
	} else {
		n.RuneCount = count
	}
	n.Original = text
	n.ByteCount = len(text)

	// NFKC first: it folds fullwidth forms, mathematical alphanumerics and
	// ligatures onto ASCII, which is what most "fancy text" obfuscations rely
	// on. Homoglyph folding then handles the letters NFKC deliberately keeps
	// apart (Cyrillic а, Greek ο...).
	folded := norm.NFKC.String(text)

	var b strings.Builder
	b.Grow(len(folded))
	lastSpace := true
	for _, r := range folded {
		switch {
		case isInvisible(r):
			n.InvisibleCount++
			continue
		case isControl(r):
			n.ControlCount++
			continue
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		if latin, ok := homoglyphs[r]; ok {
			n.HomoglyphCount++
			r = latin
		}
		b.WriteRune(unicode.ToLower(r))
		lastSpace = false
	}
	n.Canonical = strings.TrimSpace(b.String())
	return n
}

func truncateRunes(s string, max int) string {
	i := 0
	for pos := range s {
		if i == max {
			return s[:pos]
		}
		i++
	}
	return s
}

// isInvisible reports characters that render as nothing and are used to split
// keywords ("ig​nore") or to smuggle text (Unicode tag characters).
func isInvisible(r rune) bool {
	switch {
	case r == 0x00AD, // soft hyphen
		r == 0x034F,                  // combining grapheme joiner
		r == 0x061C,                  // arabic letter mark
		r == 0x180E,                  // mongolian vowel separator
		r >= 0x200B && r <= 0x200F,   // zero width space/joiners, marks
		r >= 0x202A && r <= 0x202E,   // bidi embeddings and overrides
		r >= 0x2060 && r <= 0x2064,   // word joiner, invisible operators
		r >= 0x2066 && r <= 0x206F,   // bidi isolates, deprecated format chars
		r == 0xFEFF,                  // byte order mark
		r >= 0xFE00 && r <= 0xFE0F,   // variation selectors
		r >= 0xE0000 && r <= 0xE007F: // tag characters
		return true
	}
	return false
}

func isControl(r rune) bool {
	if r == '\t' || r == '\n' || r == '\r' {
		return false
	}
	return r < 0x20 || (r >= 0x7F && r < 0xA0)
}

// homoglyphs maps the confusable letters most often used to defeat keyword
// filters onto their Latin look-alike. The table is deliberately short: it
// covers Cyrillic and Greek letters that are visually identical to Latin
// ones, not every confusable in Unicode's list, because a longer table starts
// rewriting legitimate Greek or Russian text.
var homoglyphs = map[rune]rune{
	// Cyrillic
	'а': 'a', 'А': 'A', 'е': 'e', 'Е': 'E', 'о': 'o', 'О': 'O',
	'р': 'p', 'Р': 'P', 'с': 'c', 'С': 'C', 'у': 'y', 'У': 'Y',
	'х': 'x', 'Х': 'X', 'і': 'i', 'І': 'I', 'ј': 'j', 'Ј': 'J',
	'ѕ': 's', 'Ѕ': 'S', 'ԁ': 'd', 'ԛ': 'q', 'ԝ': 'w', 'һ': 'h',
	'Н': 'H', 'К': 'K', 'М': 'M', 'Т': 'T', 'В': 'B', 'ԍ': 'G',
	// Greek
	'ο': 'o', 'Ο': 'O', 'α': 'a', 'Α': 'A', 'ε': 'e', 'Ε': 'E',
	'ι': 'i', 'Ι': 'I', 'κ': 'k', 'Κ': 'K', 'ν': 'v', 'Ν': 'N',
	'ρ': 'p', 'Ρ': 'P', 'τ': 't', 'Τ': 'T', 'υ': 'u', 'Υ': 'Y',
	'χ': 'x', 'Χ': 'X', 'Β': 'B', 'Ζ': 'Z', 'Η': 'H', 'Μ': 'M',
	// Latin extended look-alikes
	'ɡ': 'g', 'ɩ': 'i', 'ʏ': 'y', 'ᴀ': 'a', 'ᴇ': 'e', 'ᴏ': 'o',
}
