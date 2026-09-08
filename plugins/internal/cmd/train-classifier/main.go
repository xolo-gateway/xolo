// Command train-classifier rebuilds the Naive Bayes model embedded in the
// text-classifier plugin from the labelled corpus next to it.
//
//	go run ./plugins/internal/cmd/train-classifier          # retrain and overwrite model.json
//	go run ./plugins/internal/cmd/train-classifier -eval    # cross-validate only, write nothing
//
// The corpus is a JSONL file of {"text": ..., "label": ...} lines. Keeping it
// in the repository is what makes the model reproducible: anyone can add
// examples for a category the classifier gets wrong and regenerate the model.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"

	"github.com/xolo-gateway/xolo/plugins/internal/complexity"
)

func main() {
	corpusPath := flag.String("corpus", "plugins/internal/complexity/data/corpus.jsonl", "labelled corpus (JSONL)")
	modelPath := flag.String("out", "plugins/internal/complexity/data/model.json", "where to write the trained model")
	evalOnly := flag.Bool("eval", false, "cross-validate and report, do not write the model")
	folds := flag.Int("folds", 5, "number of folds for cross-validation")
	alpha := flag.Float64("alpha", 0.5, "Laplace smoothing")
	seed := flag.Int64("seed", 42, "shuffle seed for cross-validation")
	ngrams := flag.Int("ngrams", 1, "1 = unigrams only, 2 = unigrams and bigrams")
	flag.Parse()

	examples, err := loadCorpus(*corpusPath)
	if err != nil {
		fail(err)
	}
	fmt.Printf("corpus: %d examples, %d classes\n", len(examples), len(classCounts(examples)))
	for _, c := range sortedKeys(classCounts(examples)) {
		fmt.Printf("  %-14s %d\n", c, classCounts(examples)[c])
	}

	acc, confusions := crossValidate(examples, *folds, *alpha, *ngrams, *seed)
	fmt.Printf("\n%d-fold accuracy: %.1f%%\n", *folds, acc*100)
	if len(confusions) > 0 {
		fmt.Println("confusions (expected -> predicted: count):")
		for _, line := range confusions {
			fmt.Println("  " + line)
		}
	}

	if *evalOnly {
		return
	}

	nb := complexity.NewNaiveBayes(*alpha, *ngrams)
	nb.Train(examples)
	if err := nb.SaveModel(*modelPath); err != nil {
		fail(err)
	}
	fmt.Printf("\nmodel written to %s (vocabulary: %d features)\n", *modelPath, nb.VocabSize)
}

func loadCorpus(path string) ([]complexity.TrainingExample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []complexity.TrainingExample
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		if len(sc.Bytes()) == 0 {
			continue
		}
		var ex complexity.TrainingExample
		if err := json.Unmarshal(sc.Bytes(), &ex); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if ex.Text == "" || ex.Label == "" {
			return nil, fmt.Errorf("%s:%d: text and label are required", path, line)
		}
		out = append(out, ex)
	}
	return out, sc.Err()
}

// crossValidate trains on k-1 folds and predicts the remaining one, for each
// fold, and returns the overall accuracy plus the confusions sorted by count.
func crossValidate(examples []complexity.TrainingExample, k int, alpha float64, ngrams int, seed int64) (float64, []string) {
	shuffled := make([]complexity.TrainingExample, len(examples))
	copy(shuffled, examples)
	rand.New(rand.NewSource(seed)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	correct := 0
	confusion := map[string]int{}
	for fold := 0; fold < k; fold++ {
		var train, test []complexity.TrainingExample
		for i, ex := range shuffled {
			if i%k == fold {
				test = append(test, ex)
			} else {
				train = append(train, ex)
			}
		}
		nb := complexity.NewNaiveBayes(alpha, ngrams)
		nb.Train(train)
		for _, ex := range test {
			pred := nb.Predict(ex.Text).Class
			if pred == "" {
				pred = "(abstain)"
			}
			if pred == ex.Label {
				correct++
			} else {
				confusion[ex.Label+" -> "+pred]++
			}
		}
	}

	lines := make([]string, 0, len(confusion))
	for key, n := range confusion {
		lines = append(lines, fmt.Sprintf("%s: %d", key, n))
	}
	sort.Strings(lines)
	return float64(correct) / float64(len(examples)), lines
}

func classCounts(examples []complexity.TrainingExample) map[string]int {
	counts := map[string]int{}
	for _, ex := range examples {
		counts[ex.Label]++
	}
	return counts
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "train-classifier:", err)
	os.Exit(1)
}
