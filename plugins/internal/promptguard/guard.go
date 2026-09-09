package promptguard

import (
	"math"
	"sort"
	"sync"

	"github.com/xolo-gateway/xolo/plugins/internal/promptguard/data"
)

// Segment is one piece of the request to analyse, tagged with its origin.
type Segment struct {
	Kind SegmentKind
	Text string
}

// Options tunes a Guard. The zero value is usable.
type Options struct {
	// Rules replaces the embedded rule set. Nil means DefaultRules().
	Rules *RuleSet
	// MaxRunes bounds the text analysed per segment (0 = DefaultMaxRunes).
	MaxRunes int
	// KindWeights scales the risk of a segment by its origin. Missing kinds
	// default to 1. Tool results deserve more than 1: nobody expects a web
	// page to address the assistant.
	KindWeights map[SegmentKind]float64
	// QuoteDamping multiplies the risk of a segment whose every match sits
	// inside quotation marks introduced by a framing verb ("translate:",
	// "explain why this is an attack:"). 0 means DefaultQuoteDamping, 1
	// disables the damping.
	QuoteDamping float64
	// Model is the statistical layer. Nil means the embedded DefaultModel;
	// NoModel disables it.
	Model   *Model
	NoModel bool
	// ModelCap bounds what the model can add on its own: its probability
	// enters the noisy-OR capped at this value, so that a blocking threshold
	// above the cap always needs a rule or a structural signal as well. 0
	// means DefaultModelCap.
	ModelCap float64
	// ModelFloor is the probability under which the model says nothing.
	// Between the floor and the cap the contribution is the probability
	// itself. 0 means DefaultModelFloor.
	ModelFloor float64
}

const (
	// DefaultModelCap keeps a model-only detection under the usual blocking
	// thresholds while letting it cross the "suspicious" band.
	DefaultModelCap = 0.6
	// DefaultModelFloor ignores the model's noise on honest requests.
	DefaultModelFloor = 0.5
)

var (
	defaultModelOnce sync.Once
	defaultModel     *Model
	defaultModelErr  error
)

// DefaultModel returns the embedded model, loaded once. An invalid embedded
// model is a build error caught by the tests; at runtime it disables the
// statistical layer rather than the plugin.
func DefaultModel() (*Model, error) {
	defaultModelOnce.Do(func() {
		defaultModel, defaultModelErr = LoadModel(data.RawModel)
	})
	return defaultModel, defaultModelErr
}

// DefaultQuoteDamping halves the risk of a quoted attack: enough to keep a
// translation request under the usual thresholds, not enough to hide it.
const DefaultQuoteDamping = 0.5

// DefaultKindWeights is what the plugin ships with.
var DefaultKindWeights = map[SegmentKind]float64{
	SegmentUser:    1.0,
	SegmentHistory: 0.8,
	SegmentTool:    1.25,
}

// Guard is an immutable analyser, safe for concurrent use.
type Guard struct {
	rules        *RuleSet
	maxRunes     int
	kindWeights  map[SegmentKind]float64
	quoteDamping float64
	model        *Model
	modelCap     float64
	modelFloor   float64
}

// New builds a Guard.
func New(opts Options) *Guard {
	g := &Guard{rules: opts.Rules, maxRunes: opts.MaxRunes, kindWeights: opts.KindWeights}
	if g.rules == nil {
		g.rules = DefaultRules()
	}
	if g.kindWeights == nil {
		g.kindWeights = DefaultKindWeights
	}
	g.quoteDamping = opts.QuoteDamping
	if g.quoteDamping <= 0 {
		g.quoteDamping = DefaultQuoteDamping
	}
	if g.quoteDamping > 1 {
		g.quoteDamping = 1
	}
	if !opts.NoModel {
		g.model = opts.Model
		if g.model == nil {
			g.model, _ = DefaultModel()
		}
	}
	g.modelCap = opts.ModelCap
	if g.modelCap <= 0 {
		g.modelCap = DefaultModelCap
	}
	g.modelFloor = opts.ModelFloor
	if g.modelFloor <= 0 {
		g.modelFloor = DefaultModelFloor
	}
	return g
}

// Model exposes the active model, nil when disabled.
func (g *Guard) Model() *Model { return g.model }

// Rules exposes the active rule set.
func (g *Guard) Rules() *RuleSet { return g.rules }

// SegmentResult is the analysis of one segment.
type SegmentResult struct {
	Kind       SegmentKind `json:"kind"`
	Risk       float64     `json:"risk"`
	RuleScore  float64     `json:"rule_score"`
	Structural Structural  `json:"structural"`
	Matches    []Match     `json:"matches,omitempty"`
	// Quoted is true when every match sat inside framed quotation marks and
	// the risk was damped accordingly.
	Quoted bool `json:"quoted,omitempty"`
	// ModelProbability is the raw output of the statistical layer, before
	// floor and cap. 0 when the model is disabled.
	ModelProbability float64 `json:"model_probability"`
}

// Assessment is the outcome of Assess. Every number lives in [0, 1].
type Assessment struct {
	// Risk is the highest segment risk. Segments are not combined by
	// noisy-OR: a request with ten clean tool results and one bad one is
	// exactly as dangerous as the bad one, not more.
	Risk float64 `json:"risk"`
	// Categories scores each category by noisy-OR over all matches.
	Categories map[Category]float64 `json:"categories"`
	// Matches lists every rule that fired, strongest first.
	Matches []Match `json:"matches,omitempty"`
	// TopRule is the id of the strongest match, "" when nothing fired.
	TopRule string `json:"top_rule,omitempty"`
	// Segment is the kind of the segment that carries Risk.
	Segment SegmentKind `json:"segment,omitempty"`
	// Quoted reports that the segment carrying Risk was damped because its
	// matches were all quoted.
	Quoted bool `json:"quoted,omitempty"`
	// ModelProbability is the highest raw model output over the segments.
	ModelProbability float64         `json:"model_probability"`
	ModelVersion     string          `json:"model_version,omitempty"`
	Segments         []SegmentResult `json:"segments,omitempty"`
	// Structural aggregates the signals over all segments.
	Structural   Structural `json:"structural"`
	RulesVersion string     `json:"rules_version"`
}

// CategoryList returns the categories with a non-zero score, strongest first.
func (a Assessment) CategoryList() []Category {
	var out []Category
	for c, s := range a.Categories {
		if s > 0 {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if a.Categories[out[i]] == a.Categories[out[j]] {
			return out[i] < out[j]
		}
		return a.Categories[out[i]] > a.Categories[out[j]]
	})
	return out
}

// Assess analyses the segments. Empty segments are skipped.
func (g *Guard) Assess(segments []Segment) Assessment {
	out := Assessment{RulesVersion: g.rules.Version, Categories: map[Category]float64{}}
	if g.model != nil {
		out.ModelVersion = g.model.Version
	}
	var all []Match
	for _, seg := range segments {
		if seg.Text == "" {
			continue
		}
		res := g.assessSegment(seg)
		out.Segments = append(out.Segments, res)
		all = append(all, res.Matches...)
		out.Structural.add(res.Structural)
		if res.ModelProbability > out.ModelProbability {
			out.ModelProbability = res.ModelProbability
		}
		if res.Risk > out.Risk || out.Segment == "" {
			out.Risk = res.Risk
			out.Segment = res.Kind
			out.Quoted = res.Quoted
		}
	}
	if len(out.Segments) == 0 {
		out.Segment = ""
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Weight > all[j].Weight })
	out.Matches = all
	if len(all) > 0 {
		out.TopRule = all[0].RuleID
	}
	out.Categories = CategoryScores(all)
	// Structural evidence is obfuscation by nature, whichever words it hides.
	if s := out.Structural.Score(); s > 0 {
		out.Categories[CategoryObfuscation] = noisyOr([]float64{out.Categories[CategoryObfuscation], s})
	}
	return out
}

func (g *Guard) assessSegment(seg Segment) SegmentResult {
	n := Normalize(seg.Text, g.maxRunes)
	st := Analyze(n)
	matches := g.rules.Evaluate(n.Canonical, seg.Kind)

	// A payload that decodes to an instruction is worth the instruction:
	// rules run on the decoded text too, flagged so the reader knows where
	// the match came from.
	for _, dec := range st.Decoded {
		dn := Normalize(dec, g.maxRunes)
		for _, m := range g.rules.Evaluate(dn.Canonical, seg.Kind) {
			m.Decoded = true
			matches = append(matches, m)
		}
	}
	// Same treatment for leetspeak and spelled-out words: the rules see the
	// word the attacker meant.
	if st.Defolded != "" {
		for _, m := range g.rules.Evaluate(st.Defolded, seg.Kind) {
			m.Decoded = true
			matches = append(matches, m)
		}
	}
	if st.Rot13 != "" {
		for _, m := range g.rules.Evaluate(st.Rot13, seg.Kind) {
			m.Decoded = true
			matches = append(matches, m)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Weight > matches[j].Weight })

	ruleScore := RuleScore(matches)
	evidence := []float64{ruleScore, st.Score()}

	// The model reads the same canonical text, plus the unfolded one when
	// there is one: leetspeak fools its n-grams as much as the rules.
	var prob float64
	if g.model != nil {
		prob = g.model.Predict(n.Canonical)
		if st.Defolded != "" {
			if p := g.model.Predict(st.Defolded); p > prob {
				prob = p
			}
		}
		if prob >= g.modelFloor {
			evidence = append(evidence, math.Min(prob, g.modelCap))
		}
	}
	risk := noisyOr(evidence)

	// Damping applies to the whole, model included: quotation marks explain
	// the model's excitement exactly as they explain the rules' matches.
	quoted := false
	if g.quoteDamping < 1 && len(matches) > 0 && allQuoted(n.Canonical, matches) {
		risk *= g.quoteDamping
		quoted = true
	}
	if w, ok := g.kindWeights[seg.Kind]; ok && w != 1 {
		risk = clamp01(risk * w)
	}
	return SegmentResult{Kind: seg.Kind, Risk: risk, RuleScore: ruleScore, Structural: st, Matches: matches, Quoted: quoted, ModelProbability: prob}
}

func (s *Structural) add(o Structural) {
	s.InvisibleCount += o.InvisibleCount
	s.ControlCount += o.ControlCount
	s.HomoglyphCount += o.HomoglyphCount
	s.EncodedPayloads += o.EncodedPayloads
	s.RoleMarkers += o.RoleMarkers
	s.RepeatedLines += o.RepeatedLines
	s.LeetWords += o.LeetWords
	s.SpacedRuns += o.SpacedRuns
	s.InputRunes += o.InputRunes
	s.Truncated = s.Truncated || o.Truncated
	if o.NonAlnumRatio > s.NonAlnumRatio {
		s.NonAlnumRatio = o.NonAlnumRatio
	}
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
