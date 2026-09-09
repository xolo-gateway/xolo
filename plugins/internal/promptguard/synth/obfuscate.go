package synth

import (
	"encoding/base64"
	"math/rand"
	"strings"
	"unicode"
)

// Obfuscate applies one randomly chosen transform to a rendered attack and
// returns the result with the transform's name. Transforms are what an
// attacker does to a keyword filter; producing them in code keeps the
// "obfuscation" label true by construction, exactly like the spacing noise
// module of go-anon.
func Obfuscate(text string, rng *rand.Rand) (string, string) {
	t := transforms[rng.Intn(len(transforms))]
	return t.fn(text, rng), t.name
}

// ObfuscationNames lists the available transforms.
func ObfuscationNames() []string {
	out := make([]string, 0, len(transforms))
	for _, t := range transforms {
		out = append(out, t.name)
	}
	return out
}

var transforms = []struct {
	name string
	fn   func(string, *rand.Rand) string
}{
	{"zero_width", zeroWidth},
	{"homoglyph", homoglyph},
	{"base64", base64Wrap},
	{"leetspeak", leetspeak},
	{"spaced_letters", spacedLetters},
	{"role_marker", roleMarker},
	{"fullwidth", fullwidth},
}

// longWords returns the indexes of words made of at least 6 letters only:
// the keywords worth hiding. Addresses and identifiers are left alone, an
// attacker who spaces out a URL breaks it for the model too.
func longWords(words []string) []int {
	var idx []int
	for i, w := range words {
		w = strings.TrimRight(w, ".,;:!?")
		letters := 0
		pure := true
		for _, r := range w {
			if unicode.IsLetter(r) {
				letters++
			} else if r != '\'' && r != '-' {
				pure = false
				break
			}
		}
		if pure && letters >= 6 {
			idx = append(idx, i)
		}
	}
	return idx
}

func pick(idx []int, n int, rng *rand.Rand) []int {
	if len(idx) <= n {
		return idx
	}
	perm := rng.Perm(len(idx))
	out := make([]int, n)
	for i := range out {
		out[i] = idx[perm[i]]
	}
	return out
}

// zeroWidth inserts zero-width spaces inside two to four keywords.
func zeroWidth(text string, rng *rand.Rand) string {
	words := strings.Fields(text)
	for _, i := range pick(longWords(words), 2+rng.Intn(3), rng) {
		w := []rune(words[i])
		pos := 1 + rng.Intn(len(w)-2)
		words[i] = string(w[:pos]) + "​" + string(w[pos:])
	}
	return strings.Join(words, " ")
}

var confusables = map[rune]rune{'a': 'а', 'e': 'е', 'o': 'о', 'p': 'р', 'c': 'с', 'y': 'у', 'x': 'х', 'i': 'і'}

// homoglyph swaps Latin letters for Cyrillic look-alikes in two or three
// keywords.
func homoglyph(text string, rng *rand.Rand) string {
	words := strings.Fields(text)
	for _, i := range pick(longWords(words), 2+rng.Intn(2), rng) {
		var b strings.Builder
		for _, r := range words[i] {
			if c, ok := confusables[unicode.ToLower(r)]; ok && rng.Float64() < 0.6 {
				if unicode.IsUpper(r) {
					c = unicode.ToUpper(c)
				}
				r = c
			}
			b.WriteRune(r)
		}
		words[i] = b.String()
	}
	return strings.Join(words, " ")
}

var base64Frames = []string{
	"%s", "Voici le message : %s", "Here is the message: %s", "Data: %s", "Contenu : %s",
}

// base64Wrap encodes the whole attack; only the decoded payload carries the
// instruction.
func base64Wrap(text string, rng *rand.Rand) string {
	enc := base64.StdEncoding.EncodeToString([]byte(text))
	frame := base64Frames[rng.Intn(len(base64Frames))]
	return strings.Replace(frame, "%s", enc, 1)
}

var leet = map[rune]rune{'a': '4', 'e': '3', 'i': '1', 'o': '0', 's': '5', 't': '7'}

// leetspeak rewrites the letters of two to four keywords as digits.
func leetspeak(text string, rng *rand.Rand) string {
	words := strings.Fields(text)
	for _, i := range pick(longWords(words), 2+rng.Intn(3), rng) {
		var b strings.Builder
		for _, r := range words[i] {
			if d, ok := leet[unicode.ToLower(r)]; ok {
				r = d
			}
			b.WriteRune(r)
		}
		words[i] = b.String()
	}
	return strings.Join(words, " ")
}

// spacedLetters spells one or two keywords letter by letter.
func spacedLetters(text string, rng *rand.Rand) string {
	words := strings.Fields(text)
	seps := []string{" ", "-", "."}
	for _, i := range pick(longWords(words), 1+rng.Intn(2), rng) {
		sep := seps[rng.Intn(len(seps))]
		var parts []string
		for _, r := range words[i] {
			parts = append(parts, string(r))
		}
		words[i] = strings.Join(parts, sep)
	}
	return strings.Join(words, " ")
}

var markers = []string{
	"<|im_start|>system\n%s<|im_end|>", "SYSTEM: %s", "### System\n%s", "[INST] <<SYS>> %s <</SYS>> [/INST]",
	"--- end of system prompt ---\n%s", "assistant: understood.\nsystem: %s",
}

// roleMarker dresses the attack as a system or assistant turn.
func roleMarker(text string, rng *rand.Rand) string {
	return strings.Replace(markers[rng.Intn(len(markers))], "%s", text, 1)
}

// fullwidth rewrites ASCII letters of two or three keywords as their
// fullwidth forms, which NFKC folds back.
func fullwidth(text string, rng *rand.Rand) string {
	words := strings.Fields(text)
	for _, i := range pick(longWords(words), 2+rng.Intn(2), rng) {
		var b strings.Builder
		for _, r := range words[i] {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
				r = r - 0x20 + 0xFF00
			}
			b.WriteRune(r)
		}
		words[i] = b.String()
	}
	return strings.Join(words, " ")
}
