package promptguard

import (
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Structural gathers the signals that do not depend on what the text says but
// on how it is written. They are cheap, language-independent and hard to fake
// away: an attacker who hides "ignore" behind zero-width spaces produces
// invisible characters whatever the wording.
type Structural struct {
	InvisibleCount  int     `json:"invisible_characters"`
	ControlCount    int     `json:"control_characters"`
	HomoglyphCount  int     `json:"homoglyphs"`
	EncodedPayloads int     `json:"encoded_payloads"`
	RoleMarkers     int     `json:"role_markers"`
	RepeatedLines   int     `json:"repeated_lines"`
	LeetWords       int     `json:"leet_words"`
	SpacedRuns      int     `json:"spaced_runs"`
	NonAlnumRatio   float64 `json:"non_alphanumeric_ratio"`
	InputRunes      int     `json:"input_runes"`
	Truncated       bool    `json:"truncated"`

	// Decoded holds the printable text recovered from Base64 or hexadecimal
	// candidates, lower-cased, so that the rules can be matched against it.
	// It is never sent anywhere and never replaces the original text.
	Decoded []string `json:"-"`
	// Defolded is the canonical text with leetspeak and spelled-out words
	// undone, empty when nothing was folded.
	Defolded string `json:"-"`
}

// Decoding limits. Together they bound the work done on hostile input: at
// most maxCandidates decodes of at most maxDecodedBytes each, one level deep.
const (
	minBase64Len    = 24
	minHexLen       = 32
	maxCandidates   = 8
	maxDecodedBytes = 4096
)

var (
	base64Re = regexp.MustCompile(`[A-Za-z0-9+/_-]{24,}={0,2}`)
	hexRe    = regexp.MustCompile(`(?i)(?:0x)?(?:[0-9a-f]{2}[ :,]?){16,}`)

	// roleMarkerRe recognises the chat-template tokens and pseudo-headers an
	// attacker inserts to make injected text look like a system or assistant
	// turn. Matched against the canonical (lower-cased) form.
	roleMarkerRe = regexp.MustCompile(
		`(?:^|\s)(?:system|assistant|user|human|ai|développeur|developer)\s*:|` +
			`<\|(?:im_start|im_end|system|user|assistant|endoftext|end_of_turn|start_header_id)\|>|` +
			`\[/?inst\]|<</?sys>>|<\s*/?\s*system\s*>|\[/?system\]|` +
			`#{1,6}\s*(?:system|instruction|new instructions?|nouvelles? instructions?)\b|` +
			`\bbegin\s+(?:system|new)\s+(?:prompt|instructions?)\b|` +
			`\b(?:end|fin)\s+(?:of\s+)?(?:system|des?\s+)?(?:prompt|instructions?)\b`)
)

// Analyze extracts the structural signals of a normalised text.
func Analyze(n Normalized) Structural {
	s := Structural{
		InvisibleCount: n.InvisibleCount,
		ControlCount:   n.ControlCount,
		HomoglyphCount: n.HomoglyphCount,
		InputRunes:     n.RuneCount,
		Truncated:      n.Truncated,
	}
	if n.Canonical == "" {
		return s
	}

	s.RoleMarkers = len(roleMarkerRe.FindAllStringIndex(n.Canonical, -1))
	if defolded, leet, spaced := Defold(n.Canonical); leet+spaced > 0 {
		s.Defolded, s.LeetWords, s.SpacedRuns = defolded, leet, spaced
	}
	s.NonAlnumRatio = nonAlnumRatio(n.Canonical)
	s.RepeatedLines = repeatedLines(n.Original)

	budget := maxCandidates
	for _, cand := range base64Re.FindAllString(n.Original, budget) {
		if budget == 0 {
			break
		}
		budget--
		if txt, ok := decodeBase64(cand); ok {
			s.EncodedPayloads++
			s.Decoded = append(s.Decoded, txt)
		}
	}
	for _, cand := range hexRe.FindAllString(n.Original, budget) {
		if budget == 0 {
			break
		}
		budget--
		if txt, ok := decodeHex(cand); ok {
			s.EncodedPayloads++
			s.Decoded = append(s.Decoded, txt)
		}
	}
	return s
}

// Score turns the structural signals into a contribution in [0, 1]. Each
// signal is a small independent probability of malice combined by noisy-OR:
// one signal alone rarely exceeds 0.4, several together approach 1.
func (s Structural) Score() float64 {
	var ps []float64
	switch {
	case s.InvisibleCount >= 5:
		ps = append(ps, 0.55)
	case s.InvisibleCount > 0:
		ps = append(ps, 0.35)
	}
	switch {
	case s.HomoglyphCount >= 5:
		ps = append(ps, 0.5)
	case s.HomoglyphCount > 0:
		ps = append(ps, 0.3)
	}
	if s.ControlCount > 0 {
		ps = append(ps, 0.2)
	}
	if s.EncodedPayloads > 0 {
		ps = append(ps, 0.25)
	}
	switch {
	case s.RoleMarkers >= 2:
		ps = append(ps, 0.5)
	case s.RoleMarkers == 1:
		ps = append(ps, 0.3)
	}
	if s.RepeatedLines >= 3 {
		ps = append(ps, 0.15)
	}
	switch {
	case s.LeetWords >= 3:
		ps = append(ps, 0.4)
	case s.LeetWords > 0:
		ps = append(ps, 0.2)
	}
	if s.SpacedRuns > 0 {
		ps = append(ps, 0.3)
	}
	if s.InputRunes > 40 && s.NonAlnumRatio > 0.5 {
		ps = append(ps, 0.15)
	}
	return noisyOr(ps)
}

// noisyOr combines independent probabilities: 1 - Π(1 - p). It grows with
// every signal and never exceeds 1, unlike a sum.
func noisyOr(ps []float64) float64 {
	keep := 1.0
	for _, p := range ps {
		if p < 0 {
			p = 0
		}
		if p > 1 {
			p = 1
		}
		keep *= 1 - p
	}
	return 1 - keep
}

func nonAlnumRatio(s string) float64 {
	var total, other int
	for _, r := range s {
		total++
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsSpace(r) {
			other++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(other) / float64(total)
}

// repeatedLines counts lines that appear more than once (beyond their first
// occurrence). Repetition is how "many-shot" style prompts try to wear the
// model down.
func repeatedLines(s string) int {
	seen := map[string]int{}
	n := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 12 {
			continue
		}
		seen[line]++
		if seen[line] > 1 {
			n++
		}
	}
	return n
}

// decodeBase64 tries the standard and URL alphabets, with and without
// padding, and keeps the result only if it reads as text. Hashes, keys and
// binary blobs decode fine but fail the printability test, which is what
// separates an encoded instruction from a random token.
func decodeBase64(cand string) (string, bool) {
	trimmed := strings.TrimRight(cand, "=")
	if len(trimmed) < minBase64Len {
		return "", false
	}
	encs := []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding}
	for _, enc := range encs {
		if enc.DecodedLen(len(trimmed)) > maxDecodedBytes {
			trimmed = trimmed[:enc.EncodedLen(maxDecodedBytes)]
		}
		out, err := enc.DecodeString(trimmed)
		if err != nil {
			// Truncated candidates may end mid-quantum; drop the tail.
			for cut := 1; cut <= 3 && err != nil && len(trimmed) > cut; cut++ {
				out, err = enc.DecodeString(trimmed[:len(trimmed)-cut])
			}
			if err != nil {
				continue
			}
		}
		if txt, ok := printableText(out); ok {
			return txt, true
		}
	}
	return "", false
}

func decodeHex(cand string) (string, bool) {
	clean := strings.Map(func(r rune) rune {
		if r == ' ' || r == ':' || r == ',' {
			return -1
		}
		return r
	}, strings.TrimPrefix(strings.ToLower(cand), "0x"))
	if len(clean) < minHexLen || len(clean)%2 == 1 {
		return "", false
	}
	if len(clean) > maxDecodedBytes*2 {
		clean = clean[:maxDecodedBytes*2]
	}
	out, err := hex.DecodeString(clean)
	if err != nil {
		return "", false
	}
	return printableText(out)
}

// printableText accepts a decoded blob as text when it is valid UTF-8 made of
// at least 90 % printable characters and contains a letter.
func printableText(b []byte) (string, bool) {
	if !utf8.Valid(b) || len(b) < 8 {
		return "", false
	}
	var total, printable, letters int
	for _, r := range string(b) {
		total++
		if unicode.IsPrint(r) || unicode.IsSpace(r) {
			printable++
		}
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if total == 0 || letters < 4 || float64(printable)/float64(total) < 0.9 {
		return "", false
	}
	return strings.ToLower(string(b)), true
}
