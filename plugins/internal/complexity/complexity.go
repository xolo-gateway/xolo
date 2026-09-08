package complexity

import (
	"bytes"
	"compress/gzip"
	"math"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// Score holds the detailed complexity analysis of a prompt.
type Score struct {
	// Individual metrics (each normalized 0–1)
	LengthScore      float64 `json:"length_score"`
	EntropyScore     float64 `json:"entropy_score"`
	LexicalRichness  float64 `json:"lexical_richness"`
	CompressionScore float64 `json:"compression_score"`
	StructuralScore  float64 `json:"structural_score"`
	ReadabilityScore float64 `json:"readability_score"`
	ConstraintScore  float64 `json:"constraint_score"`

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
	MaxNestingDepth  int     `json:"max_nesting_depth"`
	ConstraintCount  int     `json:"constraint_count"`
	QuestionCount    int     `json:"question_count"`
	ShannonEntropy   float64 `json:"shannon_entropy"`
	CompressionRatio float64 `json:"compression_ratio"`
	FleschKincaid    float64 `json:"flesch_kincaid_grade"`
	MarkdownElements int     `json:"markdown_elements"`
}

// Weights controls the relative importance of each metric.
type Weights struct {
	Length      float64
	Entropy     float64
	Lexical     float64
	Compression float64
	Structural  float64
	Readability float64
	Constraint  float64
}

// DefaultWeights returns sensible defaults calibrated for LLM prompt routing.
func DefaultWeights() Weights {
	return Weights{
		Length:      0.10,
		Entropy:     0.15,
		Lexical:     0.15,
		Compression: 0.10,
		Structural:  0.20,
		Readability: 0.10,
		Constraint:  0.20,
	}
}

// ---------- Package-level compiled regexes (compiled once at startup) ----------

var (
	reSentences = regexp.MustCompile(`[.!?]+[\s]+|[.!?]+$|\n{2,}`)
	reQuestion  = regexp.MustCompile(`\?`)

	// constraintPatterns detects explicit constraints/instructions in the prompt.
	constraintPatterns = []*regexp.Regexp{
		// Format constraints
		regexp.MustCompile(`(?i)\b(en|au|in)\s+(format|JSON|CSV|XML|YAML|markdown|HTML)\b`),
		regexp.MustCompile(`(?i)\b(moins de|plus de|maximum|minimum|at most|at least|no more than|between)\s+\d+`),
		regexp.MustCompile(`(?i)\b(mot[s]?|word[s]?|ligne[s]?|line[s]?|caractère[s]?|character[s]?|token[s]?|paragraph[s]?)\b`),
		// Conditional logic
		regexp.MustCompile(`(?i)\b(si|if|lorsque|when|unless|sauf si|à condition)\b.*\b(alors|then|sinon|else|otherwise)\b`),
		// Explicit instructions
		regexp.MustCompile(`(?i)\b(tu dois|you must|il faut|ensure|make sure|assure|n'utilise pas|do not use|don't use|avoid)\b`),
		// Enumerations / multi-step ((?m) so ^ matches each line)
		regexp.MustCompile(`(?im)(^\s*[\-\*]\s|^\s*\d+[\.\)]\s)`),
		// Role assignment
		regexp.MustCompile(`(?i)\b(agis comme|act as|tu es|you are|behave as|play the role)\b`),
		// Output structure
		regexp.MustCompile(`(?i)\b(tableau|table|liste|list|bullet|headers?|titre[s]?|section[s]?)\b`),
		// Language constraint
		regexp.MustCompile(`(?i)\b(en français|en anglais|in english|in french|in spanish|en español)\b`),
	}

	// reMarkdownHeader matches ATX-style Markdown headers (# to ######) at line start.
	reMarkdownHeader = regexp.MustCompile(`(?m)^#{1,6}\s+\S`)
	// reMarkdownListItem matches bullet and numbered list items at line start.
	reMarkdownListItem = regexp.MustCompile(`(?m)^\s*[\-\*\+]\s|^\s*\d+[\.\)]\s`)
)

// ---------- Object pools to avoid per-call allocations ----------

var gzipBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

var gzipWriterPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(nil, gzip.BestCompression)
		return w
	},
}

// Analyze computes the full complexity score for a given prompt.
func Analyze(text string, w Weights) Score {
	tokens := tokenize(text)
	sentences := splitSentences(text)

	stats := Stats{
		TokenCount:    len(tokens),
		SentenceCount: max(len(sentences), 1),
		UniqueTokens:  countUnique(tokens),
		AvgWordLength: avgLength(tokens),
	}

	// --- Shannon entropy on character bigrams ---
	stats.ShannonEntropy = shannonEntropy(text)

	// --- Compression ratio (Kolmogorov proxy) ---
	stats.CompressionRatio = compressionRatio(text)

	// --- Nesting depth (parentheses, brackets, quotes) ---
	stats.MaxNestingDepth = nestingDepth(text)

	// --- Constraint detection ---
	stats.ConstraintCount = countConstraints(text)

	// --- Question count ---
	stats.QuestionCount = countQuestions(text)

	// --- Markdown structure (headers + list items) ---
	stats.MarkdownElements = countMarkdownElements(text)

	// --- Flesch-Kincaid grade level ---
	stats.FleschKincaid = fleschKincaid(tokens, sentences)

	// ---- Normalize each metric to 0–1 ----
	s := Score{Stats: stats}

	// Length: sigmoid-like scaling, 500 tokens ≈ 0.5
	s.LengthScore = sigmoid(float64(stats.TokenCount), 500, 0.005)

	// Entropy: meaningful only for texts of at least 50 chars; below that threshold all bigrams
	// tend to be unique (artificially high entropy on short texts like "Bonjour !").
	if len(text) >= 50 {
		s.EntropyScore = clamp(stats.ShannonEntropy/5.0, 0, 1)
	}

	// Lexical richness: type-token ratio, dampened by token count to avoid TTR bias on short texts.
	// A 1-token text has TTR=1 by definition; the dampener scales it to near-zero until 20+ tokens.
	if len(tokens) > 0 {
		ttr := float64(stats.UniqueTokens) / float64(len(tokens))
		dampener := clamp(float64(len(tokens))/20.0, 0, 1)
		s.LexicalRichness = clamp(ttr*dampener, 0, 1)
	}

	// Compression: meaningful only for texts of at least 50 bytes (avoids gzip header overhead).
	// Higher ratio = harder to compress = more complex.
	s.CompressionScore = clamp(stats.CompressionRatio, 0, 1)

	// Structural: nesting + questions + sentence count + markdown structure
	nestNorm := sigmoid(float64(stats.MaxNestingDepth), 3, 0.8)
	questNorm := sigmoid(float64(stats.QuestionCount), 3, 0.5)
	sentNorm := sigmoid(float64(stats.SentenceCount), 10, 0.15)
	mdNorm := sigmoid(float64(stats.MarkdownElements), 8, 0.25)
	s.StructuralScore = clamp((nestNorm+questNorm+sentNorm+mdNorm)/4.0, 0, 1)

	// Readability: FK grade level, ~16 = very complex
	s.ReadabilityScore = clamp(stats.FleschKincaid/16.0, 0, 1)

	// Constraints: 5+ constraints = saturated
	s.ConstraintScore = sigmoid(float64(stats.ConstraintCount), 3, 0.6)

	// ---- Composite ----
	s.Composite = clamp(
		w.Length*s.LengthScore+
			w.Entropy*s.EntropyScore+
			w.Lexical*s.LexicalRichness+
			w.Compression*s.CompressionScore+
			w.Structural*s.StructuralScore+
			w.Readability*s.ReadabilityScore+
			w.Constraint*s.ConstraintScore,
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
		total += len(t)
	}
	return float64(total) / float64(len(tokens))
}

// shannonEntropy computes the Shannon entropy on character bigrams.
func shannonEntropy(text string) float64 {
	if len(text) < 2 {
		return 0
	}
	lower := strings.ToLower(text)
	freq := make(map[string]int)
	total := 0
	runes := []rune(lower)
	for i := 0; i < len(runes)-1; i++ {
		bg := string(runes[i : i+2])
		freq[bg]++
		total++
	}
	if total == 0 {
		return 0
	}
	entropy := 0.0
	ft := float64(total)
	for _, c := range freq {
		p := float64(c) / ft
		if p > 0 {
			entropy -= p * math.Log2(p)
		}
	}
	return entropy
}

// compressionRatio returns the gzip compressed/original size ratio (0–1).
// A higher ratio means the text is harder to compress (more random/complex).
// Returns 0 for texts shorter than 50 bytes where gzip header overhead is misleading.
func compressionRatio(text string) float64 {
	if len(text) < 50 {
		return 0
	}
	buf := gzipBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	gz := gzipWriterPool.Get().(*gzip.Writer)
	gz.Reset(buf)
	gz.Write([]byte(text))
	gz.Close()
	ratio := float64(buf.Len()) / float64(len(text))
	gzipBufPool.Put(buf)
	gzipWriterPool.Put(gz)
	return clamp(ratio, 0, 1)
}

// nestingDepth measures max depth of (), [], {}, «», and markdown code blocks.
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

// countMarkdownElements counts ATX headers and list items as structural signals.
func countMarkdownElements(text string) int {
	headers := len(reMarkdownHeader.FindAllString(text, -1))
	items := len(reMarkdownListItem.FindAllString(text, -1))
	return headers + items
}

func countQuestions(text string) int {
	return len(reQuestion.FindAllString(text, -1))
}

// fleschKincaid computes the Flesch-Kincaid grade level.
func fleschKincaid(tokens, sentences []string) float64 {
	if len(tokens) == 0 || len(sentences) == 0 {
		return 0
	}
	syllables := 0
	for _, t := range tokens {
		syllables += countSyllables(t)
	}
	wps := float64(len(tokens)) / float64(len(sentences))
	spw := float64(syllables) / float64(len(tokens))
	grade := 0.39*wps + 11.8*spw - 15.59
	if grade < 0 {
		grade = 0
	}
	return grade
}

// countSyllables provides a rough syllable count for a word.
func countSyllables(word string) int {
	word = strings.ToLower(word)
	if len(word) <= 2 {
		return 1
	}
	vowels := "aeiouyàâéèêëïîôùûü"
	count := 0
	prev := false
	runes := []rune(word)
	for _, r := range runes {
		isV := strings.ContainsRune(vowels, r)
		if isV && !prev {
			count++
		}
		prev = isV
	}
	// Silent 'e' heuristic
	if strings.HasSuffix(word, "e") && count > 1 {
		count--
	}
	if count == 0 {
		count = 1
	}
	return count
}

func sigmoid(x, midpoint, steepness float64) float64 {
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
	case score < 0.25:
		return "trivial"
	case score < 0.45:
		return "simple"
	case score < 0.65:
		return "moderate"
	case score < 0.80:
		return "complex"
	default:
		return "very_complex"
	}
}
