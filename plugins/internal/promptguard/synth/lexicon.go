// Package synth builds the labelled corpus that trains and evaluates
// prompt-guard, on the pattern of go-anon-datasets: a language model may
// write the skeletons of attacks and hard negatives, but the code fills in
// the words, applies the obfuscations and derives the labels. The label of a
// sample is a property of its template, never a judgement of the model.
//
// A template is a small text file:
//
//	# name: override-then-leak
//	categories: prompt_injection, prompt_leakage
//	malicious: true
//	lang: fr
//	segment: user
//	---
//	{{override_verb}} {{scope}} {{instruction_noun}}. {{transition}} {{leak_request}}.
//
// Every {{slot}} is drawn from the lexicon of the template's language.
// {{a|b|c}} picks one alternative inline. {{attack_quote}} inserts a rendered
// malicious template, which is how hard negatives ("translate: ...") share
// their surface with the positives. {{benign}} inserts an ordinary request.
package synth

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed data/lexicons/*.yaml data/templates/*/*.tmpl
var embedded embed.FS

// Lexicon maps, per language, a slot name to its values.
type Lexicon map[string]map[string][]string

// SpecialSlots are resolved by the renderer, not by the lexicon.
var SpecialSlots = map[string]bool{"attack_quote": true, "benign": true}

// DefaultLexicon loads the lexicons embedded in the binary.
func DefaultLexicon() (Lexicon, error) {
	return loadLexiconFS(embedded, "data/lexicons")
}

// LoadLexiconDir loads every <lang>.yaml of a directory.
func LoadLexiconDir(dir string) (Lexicon, error) {
	return loadLexiconFS(dirFS(dir), ".")
}

func loadLexiconFS(fsys fs.FS, dir string) (Lexicon, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	lex := Lexicon{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		lang := strings.TrimSuffix(e.Name(), ".yaml")
		raw, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var slots map[string][]string
		if err := yaml.Unmarshal(raw, &slots); err != nil {
			return nil, fmt.Errorf("lexicon %s: %w", e.Name(), err)
		}
		for slot, values := range slots {
			if len(values) == 0 {
				return nil, fmt.Errorf("lexicon %s: slot %q is empty", lang, slot)
			}
			if SpecialSlots[slot] {
				return nil, fmt.Errorf("lexicon %s: slot %q is reserved", lang, slot)
			}
			for i, v := range values {
				if strings.TrimSpace(v) == "" {
					return nil, fmt.Errorf("lexicon %s: slot %q value %d is blank", lang, slot, i)
				}
			}
		}
		lex[lang] = slots
	}
	if len(lex) == 0 {
		return nil, fmt.Errorf("no lexicon found in %s", dir)
	}
	return lex, nil
}

// Languages lists the languages that have a lexicon.
func (l Lexicon) Languages() []string {
	out := make([]string, 0, len(l))
	for k := range l {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Slots lists the slot names of a language, sorted.
func (l Lexicon) Slots(lang string) []string {
	out := make([]string, 0, len(l[lang]))
	for k := range l[lang] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Has reports whether lang defines slot.
func (l Lexicon) Has(lang, slot string) bool {
	_, ok := l[lang][slot]
	return ok
}

// Spec renders the slot list of a language for a prompt: name and two
// example values each, enough for the author to know what a slot sounds
// like without seeing the whole lexicon.
func (l Lexicon) Spec(lang string) string {
	var b strings.Builder
	for _, s := range l.Slots(lang) {
		vals := l[lang][s]
		ex := vals[0]
		if len(vals) > 1 {
			ex += " | " + vals[1]
		}
		fmt.Fprintf(&b, "- {{%s}} : %s\n", s, ex)
	}
	return b.String()
}
