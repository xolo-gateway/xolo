package synth

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xolo-gateway/xolo/plugins/internal/promptguard"
)

// Template is a parsed, validated skeleton.
type Template struct {
	Name       string
	Categories []promptguard.Category
	Malicious  bool
	Lang       string
	Segment    promptguard.SegmentKind
	// Obfuscate allows the renderer to produce obfuscated variants. Defaults
	// to Malicious: honest users do not hide their words.
	Obfuscate bool
	Body      string
	Slots     []string // distinct slot names, in order of appearance
	Source    string   // the file content, as parsed
}

var (
	slotRe = regexp.MustCompile(`\{\{([^{}]+)\}\}`)
	nameRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// Parse reads a template. It refuses anything ambiguous: an unknown header
// field, a category outside the taxonomy, a slot the lexicon does not define.
// A template accepted here renders without surprise.
func Parse(src string, lex Lexicon) (*Template, error) {
	head, body, ok := strings.Cut(src, "\n---\n")
	if !ok {
		return nil, fmt.Errorf("missing '---' separator between header and body")
	}
	t := &Template{Source: src, Body: strings.TrimSpace(body)}
	if t.Body == "" {
		return nil, fmt.Errorf("empty body")
	}
	seen := map[string]bool{}
	obfuscateSet := false
	for i, line := range strings.Split(head, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if v, ok := cutField(strings.TrimLeft(line, "# "), "name"); ok {
				t.Name = strings.ToLower(v)
				continue
			}
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("header line %d: expected 'key: value'", i+1)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if seen[key] {
			return nil, fmt.Errorf("header: duplicate field %q", key)
		}
		seen[key] = true
		switch key {
		case "categories":
			for _, c := range strings.Split(val, ",") {
				c = strings.TrimSpace(c)
				if c == "" {
					continue
				}
				cat := promptguard.Category(c)
				if !knownCategory(cat) {
					return nil, fmt.Errorf("header: unknown category %q (allowed: %s)", c, joinCategories())
				}
				t.Categories = append(t.Categories, cat)
			}
		case "malicious":
			switch val {
			case "true":
				t.Malicious = true
			case "false":
				t.Malicious = false
			default:
				return nil, fmt.Errorf("header: malicious must be true or false, got %q", val)
			}
		case "lang":
			t.Lang = val
		case "segment":
			switch promptguard.SegmentKind(val) {
			case promptguard.SegmentUser, promptguard.SegmentTool, promptguard.SegmentHistory:
				t.Segment = promptguard.SegmentKind(val)
			default:
				return nil, fmt.Errorf("header: segment must be user, tool or history, got %q", val)
			}
		case "obfuscate":
			obfuscateSet = true
			switch val {
			case "true":
				t.Obfuscate = true
			case "false":
				t.Obfuscate = false
			default:
				return nil, fmt.Errorf("header: obfuscate must be true or false, got %q", val)
			}
		default:
			return nil, fmt.Errorf("header: unknown field %q (allowed: categories, malicious, lang, segment, obfuscate)", key)
		}
	}
	if t.Name == "" {
		return nil, fmt.Errorf("missing '# name:' line")
	}
	if !nameRe.MatchString(t.Name) {
		return nil, fmt.Errorf("name %q: use lower-case words separated by hyphens", t.Name)
	}
	if !seen["malicious"] {
		return nil, fmt.Errorf("header: missing 'malicious'")
	}
	if t.Malicious && len(t.Categories) == 0 {
		return nil, fmt.Errorf("a malicious template needs at least one category")
	}
	if !t.Malicious && len(t.Categories) > 0 {
		return nil, fmt.Errorf("a benign template cannot carry categories")
	}
	if t.Lang == "" {
		return nil, fmt.Errorf("header: missing 'lang'")
	}
	if _, ok := lex[t.Lang]; !ok {
		return nil, fmt.Errorf("lang %q has no lexicon (available: %s)", t.Lang, strings.Join(lex.Languages(), ", "))
	}
	if t.Segment == "" {
		t.Segment = promptguard.SegmentUser
	}
	if !obfuscateSet {
		t.Obfuscate = t.Malicious
	}

	// Braces left over once every well-formed slot is removed mean a nested
	// or unclosed slot: "{{a|. Mais {{override_verb}}" is a model confusing
	// the alternative syntax with something else.
	if rest := slotRe.ReplaceAllString(t.Body, ""); strings.ContainsAny(rest, "{}") {
		return nil, fmt.Errorf("unbalanced or nested braces: every slot must be a single {{name}} or {{a|b}} with no brace inside")
	}

	slotSeen := map[string]bool{}
	slotCount := map[string]int{}
	total := 0
	for _, loc := range slotRe.FindAllStringSubmatchIndex(t.Body, -1) {
		total++
		m := t.Body[loc[0]:loc[1]]
		name := strings.TrimSpace(t.Body[loc[2]:loc[3]])
		// A slot glued to a word ("session{{verb}}", "{{a}}{{b}}") renders as
		// one unreadable token. Require a separator on both sides.
		if loc[0] > 0 && !isSlotBoundary(t.Body[loc[0]-1], true) {
			return nil, fmt.Errorf("slot %s is glued to the text before it: put a space or punctuation between them", m)
		}
		if loc[1] < len(t.Body) && !isSlotBoundary(t.Body[loc[1]], false) {
			return nil, fmt.Errorf("slot %s is glued to the text after it: put a space or punctuation between them", m)
		}
		if strings.Contains(name, "|") {
			for _, alt := range strings.Split(name, "|") {
				alt = strings.TrimSpace(alt)
				if len([]rune(alt)) < 2 {
					return nil, fmt.Errorf("slot %s: alternative %q too short. {{a|b|c}} means 'a or b or c', each alternative is a word or a phrase", m, alt)
				}
				if lex.Has(t.Lang, alt) || SpecialSlots[alt] {
					return nil, fmt.Errorf("slot %s: %q is a slot name, it would be rendered literally. Write {{%s}} on its own, alternatives are plain words", m, alt, alt)
				}
			}
			continue
		}
		if !SpecialSlots[name] && !lex.Has(t.Lang, name) {
			return nil, fmt.Errorf("slot {{%s}}: not in the %s lexicon (available: %s)", name, t.Lang, strings.Join(lex.Slots(t.Lang), ", "))
		}
		if name == "attack_quote" {
			if t.Malicious {
				return nil, fmt.Errorf("{{attack_quote}} is reserved for benign templates")
			}
			// An attack pasted bare into a sentence is not a quotation: nothing
			// tells the reader, or the model, that it is being talked about
			// rather than issued. Require quotation marks around it.
			if !quotedAt(t.Body, loc[0], loc[1]) {
				return nil, fmt.Errorf("{{attack_quote}} must be enclosed in quotation marks, e.g. « {{attack_quote}} » or \"{{attack_quote}}\"")
			}
		}
		slotCount[name]++
		if slotCount[name] > 2 {
			return nil, fmt.Errorf("slot {{%s}} used %d times: a slot may appear at most twice", name, slotCount[name])
		}
		if !slotSeen[name] {
			slotSeen[name] = true
			t.Slots = append(t.Slots, name)
		}
	}
	if total == 0 {
		return nil, fmt.Errorf("body has no slot: a template must vary")
	}
	// A category is a claim about the text. The parser cannot read the
	// text, but it can check that the claim rests on a slot that carries it:
	// no "exfiltration" without a destination or a secret, no "obfuscation"
	// without an encoding or a role marker.
	for _, c := range t.Categories {
		evidence, known := categoryEvidence[c]
		if !known {
			continue
		}
		found := false
		for _, slot := range t.Slots {
			if evidence[slot] {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("category %s needs one of these slots in the body: %s", c, strings.Join(sortedKeys(evidence), ", "))
		}
	}
	return t, nil
}

// categoryEvidence lists, per category, the lexicon slots that justify it.
// The names are those of data/lexicons/*.yaml; a new slot that carries a
// category belongs here too.
var categoryEvidence = map[promptguard.Category]map[string]bool{
	promptguard.CategoryPromptInjection: set("override_verb", "override_de_verb", "instruction_noun", "authority", "role_marker", "address_ai", "mode", "transition"),
	promptguard.CategoryPromptLeakage:   set("leak_request"),
	promptguard.CategoryRoleHijacking:   set("persona", "no_limits", "mode", "capability_persona", "response_constraint"),
	promptguard.CategoryObfuscation:     set("encoding", "decode_verb", "role_marker"),
	promptguard.CategoryToolAbuse:       set("tool_noun", "no_confirm"),
	promptguard.CategoryExfiltration:    set("exfil_target", "secret_noun", "data_noun"),
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// quotedAt reports whether body[start:end] is directly wrapped in quotation
// marks, allowing one space on each side.
func quotedAt(body string, start, end int) bool {
	before := strings.TrimRight(body[:start], " ")
	after := strings.TrimLeft(body[end:], " ")
	opens := []string{"«", "\"", "“", "‹", "'", "„"}
	closes := []string{"»", "\"", "”", "›", "'", "“"}
	for i, o := range opens {
		if strings.HasSuffix(before, o) && strings.HasPrefix(after, closes[i]) {
			return true
		}
	}
	return false
}

// isSlotBoundary tells whether a byte may sit right before (before=true) or
// right after a slot.
func isSlotBoundary(b byte, before bool) bool {
	if b == ' ' || b == '\n' || b == '\t' {
		return true
	}
	if before {
		return strings.IndexByte("(\"'[:-/", b) >= 0 || b >= 0x80 // « “ ‹ and other multi-byte quotes
	}
	return strings.IndexByte(".,;:!?)\"'-/]", b) >= 0 || b >= 0x80 // » ” … and other multi-byte punctuation
}

func cutField(line, key string) (string, bool) {
	k, v, ok := strings.Cut(line, ":")
	if !ok || strings.TrimSpace(k) != key {
		return "", false
	}
	return strings.TrimSpace(v), true
}

func knownCategory(c promptguard.Category) bool {
	for _, k := range promptguard.Categories {
		if k == c {
			return true
		}
	}
	return false
}

func joinCategories() string {
	parts := make([]string, 0, len(promptguard.Categories))
	for _, c := range promptguard.Categories {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, ", ")
}

// DefaultTemplates loads the templates embedded in the binary.
func DefaultTemplates(lex Lexicon) ([]*Template, error) {
	return loadTemplatesFS(embedded, "data/templates", lex)
}

// LoadTemplateDir loads every <lang>/*.tmpl of a directory.
func LoadTemplateDir(dir string, lex Lexicon) ([]*Template, error) {
	return loadTemplatesFS(dirFS(dir), ".", lex)
}

func loadTemplatesFS(fsys fs.FS, root string, lex Lexicon) ([]*Template, error) {
	var out []*Template
	names := map[string]string{}
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".tmpl") {
			return nil
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		t, err := Parse(string(raw), lex)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		// Files are organised per language; the header must agree.
		if dirLang := path.Base(path.Dir(p)); dirLang != "." && dirLang != t.Lang {
			return fmt.Errorf("%s: lang %q but stored under %s/", p, t.Lang, dirLang)
		}
		key := t.Lang + "/" + t.Name
		if prev, dup := names[key]; dup {
			return fmt.Errorf("%s: name %q already used by %s", p, t.Name, prev)
		}
		names[key] = p
		out = append(out, t)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Lang != out[j].Lang {
			return out[i].Lang < out[j].Lang
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// dirFS is os.DirFS with a friendlier error on a missing directory.
func dirFS(dir string) fs.FS {
	if _, err := os.Stat(dir); err != nil {
		return failingFS{err}
	}
	return os.DirFS(filepath.Clean(dir))
}

type failingFS struct{ err error }

func (f failingFS) Open(string) (fs.File, error) { return nil, f.err }
