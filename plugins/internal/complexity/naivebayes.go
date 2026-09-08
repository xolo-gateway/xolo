package complexity

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

// --------------------------------------------------------------------------
// Multinomial Naive Bayes classifier — pure Go, zero dependencies, CPU-only.
//
// Designed for prompt categorization (code, translation, summarization,
// creative, factual, math, conversation, analysis, etc.) but generic enough
// for any text classification task.
//
// Features:
//   - Unigram + bigram tokens (configurable)
//   - Laplace (add-α) smoothing
//   - Log-space arithmetic to avoid underflow
//   - JSON serialization for trained models
//   - Thread-safe after training (read-only at inference)
// --------------------------------------------------------------------------

// NaiveBayes is a Multinomial Naive Bayes text classifier.
type NaiveBayes struct {
	// Exported for JSON serialization
	Classes    []string                  `json:"classes"`
	ClassFreq  map[string]int            `json:"class_freq"`
	TokenFreq  map[string]map[string]int `json:"token_freq"`
	ClassTotal map[string]int            `json:"class_total"`
	Vocab      map[string]struct{}       `json:"vocab"`
	VocabSize  int                       `json:"vocab_size"`
	TotalDocs  int                       `json:"total_docs"`
	Alpha      float64                   `json:"alpha"`
	UseNgrams  int                       `json:"use_ngrams"` // 1 = unigrams, 2 = uni+bigrams
}

// Prediction holds a single class prediction with its probability.
type Prediction struct {
	Class string  `json:"class"`
	Prob  float64 `json:"prob"`
}

// NewNaiveBayes creates a new untrained classifier.
// alpha is the Laplace smoothing parameter (typically 1.0).
// ngrams controls whether to use unigrams only (1) or unigrams+bigrams (2).
func NewNaiveBayes(alpha float64, ngrams int) *NaiveBayes {
	if alpha <= 0 {
		alpha = 1.0
	}
	if ngrams < 1 || ngrams > 2 {
		ngrams = 2
	}
	return &NaiveBayes{
		Classes:    make([]string, 0),
		ClassFreq:  make(map[string]int),
		TokenFreq:  make(map[string]map[string]int),
		ClassTotal: make(map[string]int),
		Vocab:      make(map[string]struct{}),
		Alpha:      alpha,
		UseNgrams:  ngrams,
	}
}

// Train feeds multiple labeled examples into the classifier.
func (nb *NaiveBayes) Train(examples []TrainingExample) {
	for _, ex := range examples {
		nb.trainOne(ex.Text, ex.Label)
	}
	nb.finalize()
}

// TrainingExample is a single labeled document.
type TrainingExample struct {
	Text  string `json:"text"`
	Label string `json:"label"`
}

// trainOne adds a single document to the model.
func (nb *NaiveBayes) trainOne(text, class string) {
	nb.TotalDocs++
	nb.ClassFreq[class]++

	if _, ok := nb.TokenFreq[class]; !ok {
		nb.TokenFreq[class] = make(map[string]int)
	}

	tokens := nb.extractFeatures(text)
	for _, t := range tokens {
		nb.TokenFreq[class][t]++
		nb.ClassTotal[class]++
		nb.Vocab[t] = struct{}{}
	}
}

// finalize computes derived fields after training.
func (nb *NaiveBayes) finalize() {
	nb.VocabSize = len(nb.Vocab)
	seen := make(map[string]bool)
	nb.Classes = nb.Classes[:0]
	for c := range nb.ClassFreq {
		if !seen[c] {
			nb.Classes = append(nb.Classes, c)
			seen[c] = true
		}
	}
	sort.Strings(nb.Classes)
}

// Predict returns the most likely class for the given text.
func (nb *NaiveBayes) Predict(text string) Prediction {
	preds := nb.PredictTopK(text, 1)
	if len(preds) == 0 {
		return Prediction{}
	}
	return preds[0]
}

// PredictTopK returns the top-k most likely classes, sorted by probability.
func (nb *NaiveBayes) PredictTopK(text string, k int) []Prediction {
	if nb.TotalDocs == 0 || len(nb.Classes) == 0 {
		return nil
	}

	tokens := nb.extractFeatures(text)
	logProbs := make(map[string]float64)

	for _, class := range nb.Classes {
		// Log prior: P(class)
		logPrior := math.Log(float64(nb.ClassFreq[class]) / float64(nb.TotalDocs))

		// Log likelihood: sum of log P(token|class) with Laplace smoothing
		logLikelihood := 0.0
		classTokens := nb.TokenFreq[class]
		denom := float64(nb.ClassTotal[class]) + nb.Alpha*float64(nb.VocabSize)

		for _, t := range tokens {
			count := 0
			if classTokens != nil {
				count = classTokens[t]
			}
			logLikelihood += math.Log((float64(count) + nb.Alpha) / denom)
		}

		logProbs[class] = logPrior + logLikelihood
	}

	// Convert log-probs to normalized probabilities via log-sum-exp
	preds := logProbsToProbs(logProbs, nb.Classes)

	sort.Slice(preds, func(i, j int) bool {
		return preds[i].Prob > preds[j].Prob
	})

	if k > len(preds) {
		k = len(preds)
	}
	return preds[:k]
}

// PredictAll returns probabilities for all classes, sorted descending.
func (nb *NaiveBayes) PredictAll(text string) []Prediction {
	return nb.PredictTopK(text, len(nb.Classes))
}

// ---------- Feature extraction ----------

func (nb *NaiveBayes) extractFeatures(text string) []string {
	words := tokenize(text) // reuse from complexity.go

	features := make([]string, 0, len(words)*2)

	// Unigrams
	for _, w := range words {
		features = append(features, w)
	}

	// Bigrams
	if nb.UseNgrams >= 2 && len(words) > 1 {
		for i := 0; i < len(words)-1; i++ {
			features = append(features, words[i]+"_"+words[i+1])
		}
	}

	return features
}

// ---------- Log-sum-exp conversion ----------

func logProbsToProbs(logProbs map[string]float64, classes []string) []Prediction {
	// Find max for numerical stability
	maxLP := math.Inf(-1)
	for _, lp := range logProbs {
		if lp > maxLP {
			maxLP = lp
		}
	}

	// Compute exp(lp - max) and sum
	sumExp := 0.0
	exps := make(map[string]float64, len(classes))
	for _, c := range classes {
		e := math.Exp(logProbs[c] - maxLP)
		exps[c] = e
		sumExp += e
	}

	// Normalize
	preds := make([]Prediction, 0, len(classes))
	for _, c := range classes {
		preds = append(preds, Prediction{
			Class: c,
			Prob:  exps[c] / sumExp,
		})
	}
	return preds
}

// ---------- Serialization ----------

// SaveModel writes the trained model to a JSON file.
func (nb *NaiveBayes) SaveModel(path string) error {
	data, err := json.MarshalIndent(nb, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal model: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// LoadModel reads a trained model from JSON data.
func LoadModel(data []byte) (*NaiveBayes, error) {
	nb := &NaiveBayes{}
	if err := json.Unmarshal(data, nb); err != nil {
		return nil, fmt.Errorf("unmarshal model: %w", err)
	}
	return nb, nil
}

// LoadModelFromFile reads a trained model from a JSON file.
func LoadModelFromFile(path string) (*NaiveBayes, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read model: %w", err)
	}

	return LoadModel(data)
}

// ---------- Convenience: combined analysis ----------

// FullAnalysis holds both complexity score and category prediction.
type FullAnalysis struct {
	Complexity    Score        `json:"complexity"`
	Category      Prediction   `json:"category"`
	TopCategories []Prediction `json:"top_categories"`
}

// AnalyzeFull runs both complexity scoring and Naive Bayes categorization.
func AnalyzeFull(text string, w Weights, nb *NaiveBayes) FullAnalysis {
	return FullAnalysis{
		Complexity:    Analyze(text, w),
		Category:      nb.Predict(text),
		TopCategories: nb.PredictTopK(text, 3),
	}
}

// ---------- Utility: accuracy evaluation ----------

// Evaluate runs the classifier against a labeled test set and returns accuracy.
func (nb *NaiveBayes) Evaluate(testSet []TrainingExample) (accuracy float64, confusionLog []string) {
	correct := 0
	for _, ex := range testSet {
		pred := nb.Predict(ex.Text)
		if pred.Class == ex.Label {
			correct++
		} else {
			confusionLog = append(confusionLog, fmt.Sprintf(
				"expected=%s got=%s (%.1f%%) text=%q",
				ex.Label, pred.Class, pred.Prob*100,
				truncate(ex.Text, 60),
			))
		}
	}
	accuracy = float64(correct) / float64(len(testSet))
	return
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ---------- Utility: print model stats ----------

func (nb *NaiveBayes) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("NaiveBayes model: %d classes, %d vocab, %d docs\n",
		len(nb.Classes), nb.VocabSize, nb.TotalDocs))
	sb.WriteString(fmt.Sprintf("Smoothing α=%.2f, ngrams=%d\n", nb.Alpha, nb.UseNgrams))
	sb.WriteString("Class distribution:\n")
	for _, c := range nb.Classes {
		sb.WriteString(fmt.Sprintf("  %-15s %4d docs, %6d tokens\n",
			c, nb.ClassFreq[c], nb.ClassTotal[c]))
	}
	return sb.String()
}
