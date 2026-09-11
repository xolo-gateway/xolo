package promptguard

import (
	_ "embed"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Category is one of the fixed families of threat. A rule belongs to exactly
// one; a text may hit several.
type Category string

const (
	CategoryPromptInjection Category = "prompt_injection"
	CategoryPromptLeakage   Category = "prompt_leakage"
	CategoryRoleHijacking   Category = "role_hijacking"
	CategoryObfuscation     Category = "obfuscation"
	CategoryToolAbuse       Category = "tool_abuse"
	CategoryExfiltration    Category = "exfiltration"
)

// Categories lists every category in a stable order.
var Categories = []Category{
	CategoryPromptInjection, CategoryPromptLeakage, CategoryRoleHijacking,
	CategoryObfuscation, CategoryToolAbuse, CategoryExfiltration,
}

func knownCategory(c Category) bool {
	for _, k := range Categories {
		if k == c {
			return true
		}
	}
	return false
}

// SegmentKind tells where a piece of text comes from. The same sentence is not
// equally suspicious in a user's question and in a web page returned by a
// tool.
type SegmentKind string

const (
	SegmentUser    SegmentKind = "user"    // the last user turn
	SegmentHistory SegmentKind = "history" // earlier user turns
	SegmentTool    SegmentKind = "tool"    // tool results, retrieved documents
	// SegmentConversation never carries text: it labels an Assessment whose
	// risk comes from the accumulation of turns rather than from one of them.
	SegmentConversation SegmentKind = "conversation"
)

//go:embed rules.yaml
var defaultRulesYAML []byte

// RuleSet is a compiled, validated set of rules. It is immutable after
// loading and safe for concurrent use.
type RuleSet struct {
	Version string
	Rules   []Rule
	// Disabled lists the ids declared with enabled: false. Merge uses it to
	// switch off a default rule from an extra file.
	Disabled []string
}

// Rule is one detection pattern family. All of its patterns share the weight:
// a rule counts once however many of its patterns match.
type Rule struct {
	ID          string
	Category    Category
	Description string
	Weight      float64
	// Segments restricts the rule to some segment kinds. Empty means all.
	Segments []SegmentKind
	// Triggers are literal substrings; the patterns only run when one of
	// them occurs in the text. Empty means the patterns always run. A
	// substring test costs nanoseconds per byte where the regexps cost
	// close to a microsecond, so triggers are what keeps a 100 KB document
	// affordable.
	Triggers []string
	Patterns []*regexp.Regexp
}

// Match is a rule that fired on a text.
type Match struct {
	RuleID   string   `json:"rule_id"`
	Category Category `json:"category"`
	Weight   float64  `json:"weight"`
	// Decoded is true when the rule matched inside a Base64/hex payload or
	// an unfolded word rather than in the visible text.
	Decoded bool `json:"decoded,omitempty"`
	// Start and End locate the first occurrence in the canonical text of the
	// segment. They are meaningless when Decoded is true.
	Start, End int `json:"-"`
}

type ruleFile struct {
	Version string     `yaml:"version"`
	Rules   []ruleYAML `yaml:"rules"`
}

type ruleYAML struct {
	ID          string   `yaml:"id"`
	Category    string   `yaml:"category"`
	Description string   `yaml:"description"`
	Weight      float64  `yaml:"weight"`
	Enabled     *bool    `yaml:"enabled"`
	Segments    []string `yaml:"segments"`
	Triggers    []string `yaml:"triggers"`
	Patterns    []string `yaml:"patterns"`
}

const (
	maxRules         = 500
	maxPatternLength = 2000
)

// DefaultRules returns the rule set embedded in the binary. It panics if the
// embedded file is invalid, which the tests catch before any release.
func DefaultRules() *RuleSet {
	rs, err := ParseRules(defaultRulesYAML)
	if err != nil {
		panic("promptguard: embedded rules.yaml is invalid: " + err.Error())
	}
	return rs
}

// ParseRules loads and validates a YAML rule file. Any error refuses the
// whole file: a rule set that half-loads would silently detect less than its
// author believes.
func ParseRules(data []byte) (*RuleSet, error) {
	var f ruleFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	if len(f.Rules) > maxRules {
		return nil, fmt.Errorf("rules: %d rules, at most %d allowed", len(f.Rules), maxRules)
	}
	rs := &RuleSet{Version: f.Version}
	seen := map[string]bool{}
	for i, r := range f.Rules {
		if r.ID == "" {
			return nil, fmt.Errorf("rules[%d]: missing id", i)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("rule %q: duplicate id", r.ID)
		}
		seen[r.ID] = true
		if r.Enabled != nil && !*r.Enabled {
			rs.Disabled = append(rs.Disabled, r.ID)
			continue
		}
		if !knownCategory(Category(r.Category)) {
			return nil, fmt.Errorf("rule %q: unknown category %q", r.ID, r.Category)
		}
		if r.Weight <= 0 || r.Weight > 1 {
			return nil, fmt.Errorf("rule %q: weight %v must be in (0, 1]", r.ID, r.Weight)
		}
		if len(r.Patterns) == 0 {
			return nil, fmt.Errorf("rule %q: no pattern", r.ID)
		}
		rule := Rule{
			ID:          r.ID,
			Category:    Category(r.Category),
			Description: r.Description,
			Weight:      r.Weight,
		}
		for _, s := range r.Segments {
			k := SegmentKind(s)
			if k != SegmentUser && k != SegmentHistory && k != SegmentTool {
				return nil, fmt.Errorf("rule %q: unknown segment %q", r.ID, s)
			}
			rule.Segments = append(rule.Segments, k)
		}
		for _, t := range r.Triggers {
			t = strings.ToLower(t)
			if t == "" {
				return nil, fmt.Errorf("rule %q: empty trigger", r.ID)
			}
			rule.Triggers = append(rule.Triggers, t)
		}
		for _, p := range r.Patterns {
			if p == "" {
				return nil, fmt.Errorf("rule %q: empty pattern", r.ID)
			}
			if len(p) > maxPatternLength {
				return nil, fmt.Errorf("rule %q: pattern longer than %d bytes", r.ID, maxPatternLength)
			}
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", r.ID, err)
			}
			rule.Patterns = append(rule.Patterns, re)
		}
		rs.Rules = append(rs.Rules, rule)
	}
	return rs, nil
}

// Merge returns a new set holding the rules of rs plus those of extra. A rule
// of extra with an id already present replaces the original, which is how a
// deployment tunes a default rule's weight without forking the file.
func (rs *RuleSet) Merge(extra *RuleSet) *RuleSet {
	if extra == nil {
		return rs
	}
	out := &RuleSet{Version: rs.Version}
	if extra.Version != "" {
		out.Version = rs.Version + "+" + extra.Version
	}
	override := map[string]Rule{}
	for _, r := range extra.Rules {
		override[r.ID] = r
	}
	disabled := map[string]bool{}
	for _, id := range extra.Disabled {
		disabled[id] = true
	}
	for _, r := range rs.Rules {
		if disabled[r.ID] {
			continue
		}
		if o, ok := override[r.ID]; ok {
			out.Rules = append(out.Rules, o)
			delete(override, r.ID)
			continue
		}
		out.Rules = append(out.Rules, r)
	}
	for _, r := range extra.Rules {
		if _, pending := override[r.ID]; pending {
			out.Rules = append(out.Rules, r)
		}
	}
	return out
}

// Evaluate runs every rule applicable to kind against a canonical text and
// returns the matches, strongest first.
func (rs *RuleSet) Evaluate(canonical string, kind SegmentKind) []Match {
	if canonical == "" {
		return nil
	}
	var out []Match
	for _, r := range rs.Rules {
		if !r.appliesTo(kind) || !r.triggered(canonical) {
			continue
		}
		for _, re := range r.Patterns {
			if loc := re.FindStringIndex(canonical); loc != nil {
				out = append(out, Match{RuleID: r.ID, Category: r.Category, Weight: r.Weight, Start: loc[0], End: loc[1]})
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Weight > out[j].Weight })
	return out
}

func (r Rule) triggered(text string) bool {
	if len(r.Triggers) == 0 {
		return true
	}
	for _, t := range r.Triggers {
		if strings.Contains(text, t) {
			return true
		}
	}
	return false
}

func (r Rule) appliesTo(kind SegmentKind) bool {
	if len(r.Segments) == 0 {
		return true
	}
	for _, k := range r.Segments {
		if k == kind {
			return true
		}
	}
	return false
}

// RuleScore combines matches by noisy-OR, each rule counted once. Two rules
// saying the same thing with different words therefore add less than two
// unrelated findings would, since their weights were set with that in mind.
func RuleScore(matches []Match) float64 {
	seen := map[string]bool{}
	var ps []float64
	for _, m := range matches {
		if seen[m.RuleID] {
			continue
		}
		seen[m.RuleID] = true
		ps = append(ps, m.Weight)
	}
	return noisyOr(ps)
}

// CategoryScores returns, per category, the noisy-OR of its matches.
func CategoryScores(matches []Match) map[Category]float64 {
	byCat := map[Category][]float64{}
	seen := map[string]bool{}
	for _, m := range matches {
		if seen[m.RuleID] {
			continue
		}
		seen[m.RuleID] = true
		byCat[m.Category] = append(byCat[m.Category], m.Weight)
	}
	out := make(map[Category]float64, len(byCat))
	for c, ps := range byCat {
		out[c] = noisyOr(ps)
	}
	return out
}

// IDs lists the rule identifiers, for diagnostics.
func (rs *RuleSet) IDs() []string {
	ids := make([]string, 0, len(rs.Rules))
	for _, r := range rs.Rules {
		ids = append(ids, r.ID)
	}
	return ids
}

func (rs *RuleSet) String() string {
	return fmt.Sprintf("ruleset %s (%d rules: %s)", rs.Version, len(rs.Rules), strings.Join(rs.IDs(), ", "))
}
