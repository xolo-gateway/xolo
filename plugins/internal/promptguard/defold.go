package promptguard

import (
	"strings"
	"unicode"
)

// Defold undoes two cheap keyword disguises on a canonical text: leetspeak
// ("1gn0re", "syst3m") and spelled-out words ("i-g-n-o-r-e", "s y s t e m").
// It returns the unfolded text plus how many words each transform touched,
// which are signals in their own right. The unfolded text is matched by the
// rules in addition to the canonical one, never instead of it, because
// folding digits to letters would break honest tokens such as "base64".
func Defold(canonical string) (text string, leetWords, spacedRuns int) {
	text, spacedRuns = joinSpelledOut(canonical)
	text, leetWords = unleet(text)
	return text, leetWords, spacedRuns
}

var leetDigits = map[rune]rune{'0': 'o', '1': 'i', '3': 'e', '4': 'a', '5': 's', '7': 't', '@': 'a', '$': 's'}

// leetAllow lists honest words that look like leetspeak.
var leetAllow = map[string]bool{
	"base64": true, "rot13": true, "sha256": true, "sha512": true, "md5": true, "utf8": true, "utf16": true,
	"mp3": true, "mp4": true, "h264": true, "h265": true, "2fa": true, "3d": true, "4k": true, "i18n": true,
	"l10n": true, "k8s": true, "s3": true, "ec2": true, "ipv4": true, "ipv6": true, "x86": true, "win32": true,
	"py3": true, "c3po": true, "r2d2": true, "b2b": true, "b2c": true, "p2p": true, "a4": true, "a3": true,
	"e2e": true, "y2k": true, "3g": true, "4g": true, "5g": true, "1080p": true, "720p": true, "web3": true,
}

// unleet rewrites words that mix letters and leet digits. A word qualifies
// when it has at least five characters, at least three letters, at least two
// leet characters, and is not an allowed token.
func unleet(s string) (string, int) {
	words := strings.Split(s, " ")
	n := 0
	for i, w := range words {
		core := strings.Trim(w, ".,;:!?\"'«»()")
		if len(core) < 5 || leetAllow[core] {
			continue
		}
		letters, leets := 0, 0
		for _, r := range core {
			switch {
			case unicode.IsLetter(r):
				letters++
			case leetDigits[r] != 0:
				leets++
			default:
				letters = -100 // any other character disqualifies the word
			}
		}
		if letters < 3 || leets < 2 {
			continue
		}
		n++
		words[i] = strings.Map(func(r rune) rune {
			if l, ok := leetDigits[r]; ok {
				return l
			}
			return r
		}, w)
	}
	return strings.Join(words, " "), n
}

// joinSpelledOut collapses runs of at least five single letters separated by
// the same separator (space, hyphen, dot, underscore, asterisk) into a word.
func joinSpelledOut(s string) (string, int) {
	words := strings.Split(s, " ")
	var out []string
	n := 0
	for i := 0; i < len(words); {
		// Case 1: consecutive single-letter words "i g n o r e".
		if isSingleLetter(words[i]) {
			j := i
			for j < len(words) && isSingleLetter(words[j]) {
				j++
			}
			if j-i >= 5 {
				out = append(out, strings.Join(words[i:j], ""))
				n++
				i = j
				continue
			}
		}
		// Case 2: one token "i-g-n-o-r-e" or "s.y.s.t.e.m".
		if joined, ok := unseparate(words[i]); ok {
			out = append(out, joined)
			n++
			i++
			continue
		}
		out = append(out, words[i])
		i++
	}
	return strings.Join(out, " "), n
}

func isSingleLetter(w string) bool {
	r := []rune(w)
	return len(r) == 1 && unicode.IsLetter(r[0])
}

// unseparate turns "i-g-n-o-r-e" into "ignore" when every other character
// is the same separator and there are at least five letters. Trailing
// punctuation is kept.
func unseparate(w string) (string, bool) {
	trail := ""
	for len(w) > 0 {
		last := w[len(w)-1]
		if strings.ContainsRune(".,;:!?", rune(last)) && len(w) > 1 {
			trail = string(last) + trail
			w = w[:len(w)-1]
			continue
		}
		break
	}
	r := []rune(w)
	if len(r) >= 2 && len(r)%2 == 0 && r[len(r)-1] == r[1] {
		r = r[:len(r)-1] // "i-g-n-o-r-e-" : dangling separator
	}
	if len(r) < 9 || len(r)%2 == 0 {
		return "", false
	}
	sep := r[1]
	if !strings.ContainsRune("-._*", sep) {
		return "", false
	}
	var b strings.Builder
	for i, c := range r {
		if i%2 == 1 {
			if c != sep {
				return "", false
			}
			continue
		}
		if !unicode.IsLetter(c) {
			return "", false
		}
		b.WriteRune(c)
	}
	return b.String() + trail, true
}
