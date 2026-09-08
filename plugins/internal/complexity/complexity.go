// Package complexity scores how demanding a prompt is for a language model,
// from its text alone, so that a router can pick a model of matching power.
//
// The score is built from seven signals that each answer a concrete question:
// how long is the request, how varied is its vocabulary, how much structure
// does it carry (lists, nesting, several questions), how dense is its prose,
// how many explicit constraints does it impose, does it contain code, and how
// much reasoning does it call for (proving, comparing, designing).
// Each signal is normalised to [0, 1] with a curve that starts at exactly 0,
// so an empty or trivial prompt scores 0 and nothing else. The weighted sum
// is then stretched so that a demanding request lands in the upper quarter
// of the scale, where downstream fuzzy rules expect it.
package complexity

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// Score holds the detailed complexity analysis of a prompt.
type Score struct {
	// Individual metrics (each normalized 0–1)
	LengthScore      float64 `json:"length_score"`
	LexicalRichness  float64 `json:"lexical_richness"`
	StructuralScore  float64 `json:"structural_score"`
	ReadabilityScore float64 `json:"readability_score"`
	ConstraintScore  float64 `json:"constraint_score"`
	CodeScore        float64 `json:"code_score"`
	DemandScore      float64 `json:"demand_score"`

	// Final composite score (0–1)
	Composite float64 `json:"composite"`
	// Human-readable label
	Level string `json:"level"`

	// Raw stats for transparency
	Stats Stats `json:"stats"`
}

// Stats holds raw computed statistics.
type Stats struct {
	TokenCount       int     `json:"token_count"`
	SentenceCount    int     `json:"sentence_count"`
	UniqueTokens     int     `json:"unique_tokens"`
	AvgWordLength    float64 `json:"avg_word_length"`
	WordsPerSentence float64 `json:"words_per_sentence"`
	MaxNestingDepth  int     `json:"max_nesting_depth"`
	ConstraintCount  int     `json:"constraint_count"`
	DemandCount      int     `json:"demand_count"`
	QuestionCount    int     `json:"question_count"`
	MarkdownElements int     `json:"markdown_elements"`
	CodeBlocks       int     `json:"code_blocks"`
	CodeSignals      int     `json:"code_signals"`
	HasCode          bool    `json:"has_code"`
}

// Weights controls the relative importance of each metric. They should sum to 1.
type Weights struct {
	Length      float64
	Lexical     float64
	Structural  float64
	Readability float64
	Constraint  float64
	Code        float64
	Demand      float64
}

// DefaultWeights returns the weights calibrated for LLM prompt routing.
//
// Constraints, code and cognitive demand lead: they are the signals that most
// reliably separate "answer this" from "produce something that has to satisfy
// rules" or "reason about this". Length matters less than it looks, since a
// long pasted document can carry a trivial question.
func DefaultWeights() Weights {
	return Weights{
		Length:      0.10,
		Lexical:     0.05,
		Structural:  0.10,
		Readability: 0.05,
		Constraint:  0.25,
		Code:        0.20,
		Demand:      0.25,
	}
}

// stretch is the gain applied to the weighted sum before clamping. With the
// default weights a request that saturates constraints, structure and code
// reaches 1.0; a substantive analytical request scores about 0.8; a one-line
// factual question stays under 0.2.
const stretch = 1.8

// ---------- Package-level compiled regexes (compiled once at startup) ----------

var (
	reSentences = regexp.MustCompile(`[.!?]+[\s]+|[.!?]+$|\n{2,}`)
	reQuestion  = regexp.MustCompile(`\?`)

	// constraintPatterns detects explicit constraints/instructions in the prompt.
	constraintPatterns = []*regexp.Regexp{
		// Format constraints
		regexp.MustCompile(`(?i)\b(en|au|in)\s+(format|JSON|CSV|XML|YAML|markdown|HTML)\b`),
		regexp.MustCompile(`(?i)\b(moins de|plus de|maximum|minimum|au plus|au moins|at most|at least|no more than|between|exactement|exactly)\s+\d+`),
		regexp.MustCompile(`(?i)\b\d+\s+(mots?|words?|lignes?|lines?|caractères?|characters?|tokens?|paragraphes?|paragraphs?|phrases?|sentences?|points?|bullets?|vers|étapes?|steps?)\b`),
		// Conditional logic
		regexp.MustCompile(`(?i)\b(si|if|lorsque|when|unless|sauf si|à condition)\b.*\b(alors|then|sinon|else|otherwise)\b`),
		// Explicit instructions and prohibitions
		regexp.MustCompile(`(?i)\b(tu dois|vous devez|you must|il faut|ensure|make sure|assure-toi|veille à|n'utilise pas|ne pas utiliser|do not|don't|never|jamais|avoid|évite|interdit|obligatoire|respecte|respect|en respectant|respecting|conform|conforme)\b`),
		// Enumerations / multi-step ((?m) so ^ matches each line)
		regexp.MustCompile(`(?im)(^\s*[\-\*]\s|^\s*\d+[\.\)]\s)`),
		// Inline enumerations: "1) ... 2) ... 3)"
		regexp.MustCompile(`\b\d\)\s`),
		// Role assignment
		regexp.MustCompile(`(?i)\b(agis comme|act as|tu es un|tu es une|you are a|you are an|behave as|play the role|en tant que|as a senior|as an expert)\b`),
		// Output structure
		regexp.MustCompile(`(?i)\b(tableau|table|liste|list|bullet|headers?|titres?|sections?|schéma|diagram|template|plan détaillé|outline)\b`),
		// Language, tone and audience
		regexp.MustCompile(`(?i)\b(en français|en anglais|in english|in french|in spanish|en español|ton formel|ton neutre|formal tone|informal|tutoiement|vouvoiement|pour un public|for an audience|for beginners|pour débutants)\b`),
		// Justification and sourcing
		regexp.MustCompile(`(?i)\b(cite|citer|sources?|références?|references?|justifie|justify|argumenté|argumentée|explique pourquoi|explain why|compare|comparatif|comparative|recommandations?|recommendations?|chiffré|chiffrées|quantified)\b`),
	}

	// demandPatterns detect verbs and nouns that call for reasoning rather than
	// recall: proving, comparing, designing, justifying. Each pattern counts
	// once, so a text is measured on how many kinds of demand it makes.
	demandPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(prouve|prouver|démontre|démontrer|prove|demonstrate|induction|théorème|theorem|lemma)\b`),
		regexp.MustCompile(`(?i)\b(compare|comparer|comparaison|comparatif|comparative|versus|vs\.?|compromis|trade-?offs?|avantages et inconvénients|pros and cons)\b`),
		regexp.MustCompile(`(?i)\b(analyse|analyser|analyze|analysis|évalue|évaluer|evaluate|assess|critique|critically|audit|diagnostic|diagnose)\b`),
		regexp.MustCompile(`(?i)\b(explique pourquoi|explain why|pourquoi|why|justifie|justify|argumente|argue|argumentée?|raisonnement|reasoning|implications?|conséquences|consequences)\b`),
		regexp.MustCompile(`(?i)\b(conçois|concevoir|design|architecture|architecte|spécification|specification|cahier des charges|rédige|rédiger|write a (report|spec|essay|proposal|plan)|stratégie|strategy|roadmap|plan détaillé)\b`),
		regexp.MustCompile(`(?i)\b(optimise|optimiser|optimize|améliore|improve|refactor|generali[sz]e[sd]?|généralis(e|er|ez)|extrapole|extrapolate|modélise|model the|simulate|simule)\b`),
		regexp.MustCompile(`(?i)\b(synthétise|synthèse|synthesize|synthesis|résume et compare|discuss|discute|débat|debate|nuance|limites|limitations|edge cases|cas limites)\b`),
	}

	// reMarkdownHeader matches ATX-style Markdown headers (# to ######) at line start.
	reMarkdownHeader = regexp.MustCompile(`(?m)^#{1,6}\s+\S`)
	// reMarkdownListItem matches bullet and numbered list items at line start.
	reMarkdownListItem = regexp.MustCompile(`(?m)^\s*[\-\*\+]\s|^\s*\d+[\.\)]\s`)

	// reCodeFence matches fenced code blocks (``` or ~~~).
	reCodeFence = regexp.MustCompile("(?s)(```|~~~).*?(```|~~~)")
	// codeSignalPatterns are constructs that almost never occur in prose: they
	// catch code pasted without fences, stack traces, shell commands and paths.
	codeSignalPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*(func|def|class|import|from|package|public|private|static|const|let|var|return|async|await|fn|impl|struct|enum|interface|SELECT|INSERT|UPDATE|DELETE|CREATE TABLE)\b`),
		regexp.MustCompile(`\b(if|for|while|switch|catch)\s*\(`),
		regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*\([^)]*\)`), // obj.method(args)
		regexp.MustCompile(`(?m)^\s*[}\]);]\s*$`),                                     // closing line
		regexp.MustCompile(`[=!<>]==?|&&|\|\||->|=>|::|:=`),
		regexp.MustCompile(`(?m)^\s*(\$|#|>>>|>)\s+\S`), // shell / REPL prompt
		regexp.MustCompile(`\b(Traceback|Exception|Error|panic|stack trace|at [a-zA-Z_.]+\([A-Za-z]+\.[a-z]+:\d+\))\b`),
		regexp.MustCompile(`(?i)\b(ne compile pas|does not compile|doesn't compile|erreur de compilation|compile error|segfault|null pointer|undefined is not|stacktrace|bug|debug|refactor|unit test|tests? unitaires?)\b`),
		regexp.MustCompile(`(?i)\b(golang|python|javascript|typescript|rust|java|kotlin|swift|c\+\+|c#|php|ruby|sql|bash|html|css|json|yaml|regex|api|sdk|npm|pip|cargo|docker|kubernetes|git)\b`),
	}
)

// Analyze computes the full complexity score for a given prompt.
func Analyze(text string, w Weights) Score {
	tokens := tokenize(text)
	if len(tokens) == 0 {
		return Score{Level: label(0), Stats: Stats{SentenceCount: 1}}
	}
	sentences := splitSentences(text)

	stats := Stats{
		TokenCount:    len(tokens),
		SentenceCount: max(len(sentences), 1),
		UniqueTokens:  countUnique(tokens),
		AvgWordLength: avgLength(tokens),
	}
	stats.WordsPerSentence = float64(stats.TokenCount) / float64(stats.SentenceCount)
	stats.MaxNestingDepth = nestingDepth(text)
	stats.ConstraintCount = countConstraints(text)
	stats.DemandCount = countDemands(text)
	stats.QuestionCount = countQuestions(text)
	stats.MarkdownElements = countMarkdownElements(text)
	stats.CodeBlocks = len(reCodeFence.FindAllString(text, -1))
	stats.CodeSignals = countCodeSignals(text)
	stats.HasCode = stats.CodeBlocks > 0 || stats.CodeSignals >= 3

	s := Score{Stats: stats}

	// Length: 400 words score 0.5, 1500 words saturate. Read on the whole
	// request, a long pasted document counts for what it is: more to process.
	s.LengthScore = rise(float64(stats.TokenCount), 400, 0.008)

	// Lexical richness: type-token ratio, dampened by token count so that a
	// three-word prompt (TTR = 1 by construction) does not look rich.
	if len(tokens) > 0 {
		ttr := float64(stats.UniqueTokens) / float64(len(tokens))
		dampener := clamp(float64(len(tokens))/30.0, 0, 1)
		s.LexicalRichness = clamp(ttr*dampener, 0, 1)
	}

	// Structural: nesting, several questions, many sentences, markdown layout.
	nestNorm := rise(float64(stats.MaxNestingDepth), 2, 1.0)
	questNorm := rise(float64(stats.QuestionCount), 2, 0.9)
	sentNorm := rise(float64(stats.SentenceCount), 6, 0.35)
	mdNorm := rise(float64(stats.MarkdownElements), 3, 0.7)
	s.StructuralScore = clamp((nestNorm+questNorm+sentNorm+mdNorm)/4.0, 0, 1)

	// Readability, language-agnostic: long words and long sentences both make
	// a text denser. 6 characters per word and 20 words per sentence each mark
	// the midpoint; the two are averaged.
	if stats.TokenCount > 0 {
		wordNorm := rise(stats.AvgWordLength, 6, 1.2)
		sentLenNorm := rise(stats.WordsPerSentence, 20, 0.15)
		s.ReadabilityScore = clamp((wordNorm+sentLenNorm)/2, 0, 1)
	}

	// Constraints: three explicit constraints reach the midpoint, six saturate.
	s.ConstraintScore = rise(float64(stats.ConstraintCount), 3, 0.9)

	// Code: a fenced block alone is a strong signal; scattered signals add up.
	codeUnits := float64(stats.CodeBlocks)*3 + float64(stats.CodeSignals)
	s.CodeScore = rise(codeUnits, 3, 0.8)

	// Demand: two kinds of reasoning demand score about 0.6, three about 0.9.
	s.DemandScore = rise(float64(stats.DemandCount), 1.5, 1.4)

	s.Composite = clamp(stretch*(w.Length*s.LengthScore+
		w.Lexical*s.LexicalRichness+
		w.Structural*s.StructuralScore+
		w.Readability*s.ReadabilityScore+
		w.Constraint*s.ConstraintScore+
		w.Code*s.CodeScore+
		w.Demand*s.DemandScore),
		0, 1,
	)

	s.Level = label(s.Composite)
	return s
}

// AnalyzeDefault uses the default weights.
func AnalyzeDefault(text string) Score {
	return Analyze(text, DefaultWeights())
}

// ---------- internal helpers ----------

func tokenize(text string) []string {
	f := func(c rune) bool {
		return unicode.IsSpace(c) || unicode.IsPunct(c)
	}
	raw := strings.FieldsFunc(text, f)
	out := make([]string, 0, len(raw))
	for _, w := range raw {
		w = strings.ToLower(w)
		if len(w) > 0 {
			out = append(out, w)
		}
	}
	return out
}

func splitSentences(text string) []string {
	parts := reSentences.Split(text, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) > 0 {
			out = append(out, p)
		}
	}
	return out
}

func countUnique(tokens []string) int {
	seen := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		seen[t] = struct{}{}
	}
	return len(seen)
}

func avgLength(tokens []string) float64 {
	if len(tokens) == 0 {
		return 0
	}
	total := 0
	for _, t := range tokens {
		total += len([]rune(t))
	}
	return float64(total) / float64(len(tokens))
}

// nestingDepth measures max depth of (), [], {}, «».
func nestingDepth(text string) int {
	maxD, cur := 0, 0
	openers := map[rune]rune{'(': ')', '[': ']', '{': '}', '«': '»'}
	closers := make(map[rune]bool)
	for _, c := range openers {
		closers[c] = true
	}
	for _, r := range text {
		if _, ok := openers[r]; ok {
			cur++
			if cur > maxD {
				maxD = cur
			}
		} else if closers[r] {
			if cur > 0 {
				cur--
			}
		}
	}
	return maxD
}

// countConstraints detects explicit constraints/instructions in the prompt.
func countConstraints(text string) int {
	count := 0
	for _, re := range constraintPatterns {
		count += len(re.FindAllString(text, -1))
	}
	return count
}

// countDemands counts how many distinct kinds of reasoning demand the text
// makes (proving, comparing, designing…), one point per matching pattern.
func countDemands(text string) int {
	count := 0
	for _, re := range demandPatterns {
		if re.MatchString(text) {
			count++
		}
	}
	return count
}

// countCodeSignals counts code-like constructs outside of fenced blocks, each
// pattern contributing at most a few hits so one repeated construct does not
// dominate.
func countCodeSignals(text string) int {
	unfenced := reCodeFence.ReplaceAllString(text, " ")
	count := 0
	for _, re := range codeSignalPatterns {
		count += min(len(re.FindAllString(unfenced, -1)), 4)
	}
	return count
}

// countMarkdownElements counts ATX headers and list items as structural signals.
func countMarkdownElements(text string) int {
	headers := len(reMarkdownHeader.FindAllString(text, -1))
	items := len(reMarkdownListItem.FindAllString(text, -1))
	return headers + items
}

func countQuestions(text string) int {
	return len(reQuestion.FindAllString(text, -1))
}

// rise is a logistic curve rescaled so that rise(0) = 0 and rise(+inf) = 1,
// with the given midpoint (where the raw logistic is 0.5) and steepness. It
// replaces plain sigmoids, whose non-zero value at 0 gave every prompt a
// floor score whatever its content.
func rise(x, midpoint, steepness float64) float64 {
	if x <= 0 {
		return 0
	}
	at0 := logistic(0, midpoint, steepness)
	return clamp((logistic(x, midpoint, steepness)-at0)/(1-at0), 0, 1)
}

func logistic(x, midpoint, steepness float64) float64 {
	return 1.0 / (1.0 + math.Exp(-steepness*(x-midpoint)))
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func label(score float64) string {
	switch {
	case score < 0.20:
		return "trivial"
	case score < 0.40:
		return "simple"
	case score < 0.60:
		return "moderate"
	case score < 0.80:
		return "complex"
	default:
		return "very_complex"
	}
}
