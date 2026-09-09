package synth

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xolo-gateway/xolo/plugins/internal/promptguard"
)

// Sample is one line of the corpus.
type Sample struct {
	Text      string   `json:"text"`
	Malicious bool     `json:"malicious"`
	Labels    []string `json:"labels"`
	Language  string   `json:"language"`
	// Source is the segment kind the text is meant to arrive in.
	Source string `json:"source"`
	// Family is the template name. Every variant of a template shares it, and
	// the split into train/validation/test is made by family so that no
	// paraphrase of a test attack ever sits in the training set.
	Family string `json:"family"`
	Origin string `json:"origin"`
	// Obfuscation names the transform applied, empty for none.
	Obfuscation string `json:"obfuscation,omitempty"`
	Split       string `json:"split,omitempty"`
}

// Renderer turns templates into samples.
type Renderer struct {
	lex       Lexicon
	templates []*Template
	// benign holds ordinary requests for the {{benign}} slot, per language.
	benign map[string][]string
}

// NewRenderer builds a renderer over a template set.
func NewRenderer(lex Lexicon, templates []*Template) *Renderer {
	return &Renderer{lex: lex, templates: templates, benign: map[string][]string{}}
}

// WithBenign registers ordinary requests usable by {{benign}}. The language
// is guessed crudely from the text; wrong guesses only cost a little
// realism.
func (r *Renderer) WithBenign(texts []string) *Renderer {
	for _, t := range texts {
		lang := "en"
		if looksFrench(t) {
			lang = "fr"
		}
		r.benign[lang] = append(r.benign[lang], t)
	}
	return r
}

// Options tunes a rendering campaign.
type Options struct {
	// PerTemplate is how many distinct variants to draw from each template.
	PerTemplate int
	// ObfuscatedShare is the fraction of malicious variants that also get an
	// obfuscated copy, in [0, 1].
	ObfuscatedShare float64
	Seed            int64
}

// DefaultOptions: 40 variants per template, a quarter of the attacks also
// obfuscated.
func DefaultOptions() Options {
	return Options{PerTemplate: 40, ObfuscatedShare: 0.25, Seed: 1}
}

// Render produces the corpus. Output is deterministic for a given seed and
// template set, which is what lets two people regenerate the same file.
func (r *Renderer) Render(opts Options) ([]Sample, error) {
	if opts.PerTemplate <= 0 {
		opts.PerTemplate = DefaultOptions().PerTemplate
	}
	rng := rand.New(rand.NewSource(opts.Seed))
	var out []Sample
	seen := map[string]bool{}
	for _, t := range r.templates {
		attempts := 0
		produced := 0
		for produced < opts.PerTemplate && attempts < opts.PerTemplate*4 {
			attempts++
			text, err := r.renderOnce(t, rng, 0)
			if err != nil {
				return nil, fmt.Errorf("template %s/%s: %w", t.Lang, t.Name, err)
			}
			if seen[text] {
				continue
			}
			seen[text] = true
			produced++
			s := r.sample(t, text)
			out = append(out, s)
			if t.Malicious && t.Obfuscate && rng.Float64() < opts.ObfuscatedShare {
				ob, name := Obfuscate(text, rng)
				if !seen[ob] {
					seen[ob] = true
					o := r.sample(t, ob)
					o.Obfuscation = name
					o.Labels = appendUnique(o.Labels, string(promptguard.CategoryObfuscation))
					out = append(out, o)
				}
			}
		}
	}
	for i := range out {
		out[i].Split = SplitOf(out[i].Family)
	}
	return out, nil
}

func (r *Renderer) sample(t *Template, text string) Sample {
	labels := make([]string, 0, len(t.Categories))
	for _, c := range t.Categories {
		labels = append(labels, string(c))
	}
	sort.Strings(labels)
	return Sample{
		Text: text, Malicious: t.Malicious, Labels: labels, Language: t.Lang,
		Source: string(t.Segment), Family: t.Name, Origin: "synthetic",
	}
}

// renderOnce fills every slot of a template. depth guards the one level of
// recursion {{attack_quote}} allows.
func (r *Renderer) renderOnce(t *Template, rng *rand.Rand, depth int) (string, error) {
	var err error
	text := slotRe.ReplaceAllStringFunc(t.Body, func(m string) string {
		if err != nil {
			return m
		}
		name := strings.TrimSpace(m[2 : len(m)-2])
		switch {
		case strings.Contains(name, "|"):
			alts := strings.Split(name, "|")
			return strings.TrimSpace(alts[rng.Intn(len(alts))])
		case name == "attack_quote":
			if depth > 0 {
				err = fmt.Errorf("nested {{attack_quote}}")
				return m
			}
			q, e := r.attackQuote(t.Lang, rng)
			if e != nil {
				err = e
				return m
			}
			return q
		case name == "benign":
			pool := r.benign[t.Lang]
			if len(pool) == 0 {
				pool = r.lex[t.Lang]["honest_task"]
			}
			if len(pool) == 0 {
				err = fmt.Errorf("{{benign}}: no benign text for %s", t.Lang)
				return m
			}
			return pool[rng.Intn(len(pool))]
		default:
			vals := r.lex[t.Lang][name]
			if len(vals) == 0 {
				err = fmt.Errorf("slot {{%s}} undefined", name)
				return m
			}
			return vals[rng.Intn(len(vals))]
		}
	})
	if err != nil {
		return "", err
	}
	return capitalizeSentences(text), nil
}

// attackQuote renders a random malicious template of the same language that
// itself contains no quote.
func (r *Renderer) attackQuote(lang string, rng *rand.Rand) (string, error) {
	var pool []*Template
	for _, t := range r.templates {
		if t.Malicious && t.Lang == lang && t.Segment == promptguard.SegmentUser {
			pool = append(pool, t)
		}
	}
	if len(pool) == 0 {
		return "", fmt.Errorf("{{attack_quote}}: no malicious user template in %s", lang)
	}
	q, err := r.renderOnce(pool[rng.Intn(len(pool))], rng, 1)
	if err != nil {
		return "", err
	}
	// A quote inside a sentence should not end with a period of its own.
	return strings.TrimRight(q, ". "), nil
}

// capitalizeSentences upper-cases the first letter of the text and of every
// sentence, so that lexicon values can stay lower-case and compose anywhere.
func capitalizeSentences(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	start := true
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case start && unicode.IsLetter(r):
			b.WriteRune(unicode.ToUpper(r))
			start = false
		case r == '\n':
			b.WriteRune(r)
			start = true
		case r == '.' || r == '!' || r == '?':
			b.WriteRune(r)
			// Only a punctuation followed by a space ends a sentence: ".env"
			// and "example.com" keep their case.
			if next, _ := utf8.DecodeRuneInString(s[i+size:]); next == utf8.RuneError || unicode.IsSpace(next) {
				start = true
			}
		case start && (r == ' ' || r == '"' || r == '«' || r == '\'' || r == ':' || r == '\t'):
			b.WriteRune(r)
		default:
			b.WriteRune(r)
			if !unicode.IsSpace(r) {
				start = false
			}
		}
		i += size
	}
	return b.String()
}

// SplitOf assigns a family to train, validation or test (70/15/15) from a
// hash of its name. Deterministic, and independent of the other families, so
// adding a template never moves an existing one across splits.
func SplitOf(family string) string {
	h := fnv.New32a()
	h.Write([]byte(family))
	switch n := h.Sum32() % 100; {
	case n < 70:
		return "train"
	case n < 85:
		return "validation"
	default:
		return "test"
	}
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	list = append(list, v)
	sort.Strings(list)
	return list
}

var frenchHints = []string{" le ", " la ", " les ", " des ", " est ", " une ", " un ", " et ", " que ", " pour ", " dans ", "é", "è", "ç", "à "}

func looksFrench(s string) bool {
	s = " " + strings.ToLower(s) + " "
	n := 0
	for _, h := range frenchHints {
		if strings.Contains(s, h) {
			n++
		}
	}
	return n >= 2
}
