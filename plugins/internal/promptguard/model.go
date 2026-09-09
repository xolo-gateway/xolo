package promptguard

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"sort"
	"strings"
	"unicode"
)

// Model is a logistic regression over hashed character n-grams of the
// canonical text. It is the statistical layer next to the rules: it learns
// the wordings the rules miss, and the rules keep the explanation the model
// cannot give. Training and inference share this file, so the features are
// the same by construction; there is no second implementation to keep in
// step.
type Model struct {
	Version string `json:"version"`
	// Bits is the size of the feature space as a power of two.
	Bits int `json:"bits"`
	MinN int `json:"min_n"`
	MaxN int `json:"max_n"`
	Bias float32
	// Weights holds 2^Bits values.
	Weights []float32
	// IDF holds 2^Bits inverse document frequencies computed at training
	// time. A feature seen in every example weighs nothing, one seen in a
	// handful weighs a lot; without it the model learns the function words
	// of the benign corpus instead of the wording of attacks. Nil means 1.
	IDF []float32
	// Metrics records what the trainer measured, for the record only.
	Metrics map[string]float64 `json:"metrics,omitempty"`
}

// modelJSON is the on-disk form: weights as little-endian float32 in base64,
// a quarter of the size of a JSON array and exact to the bit.
type modelJSON struct {
	Version string             `json:"version"`
	Bits    int                `json:"bits"`
	MinN    int                `json:"min_n"`
	MaxN    int                `json:"max_n"`
	Bias    float32            `json:"bias"`
	Weights string             `json:"weights"`
	IDF     string             `json:"idf,omitempty"`
	Metrics map[string]float64 `json:"metrics,omitempty"`
}

const (
	minModelBits = 8
	maxModelBits = 22
)

// MarshalJSON implements json.Marshaler.
func (m *Model) MarshalJSON() ([]byte, error) {
	j := modelJSON{
		Version: m.Version, Bits: m.Bits, MinN: m.MinN, MaxN: m.MaxN, Bias: m.Bias,
		Weights: encodeFloats(m.Weights), Metrics: m.Metrics,
	}
	if m.IDF != nil {
		j.IDF = encodeFloats(m.IDF)
	}
	return json.Marshal(j)
}

func encodeFloats(v []float32) string {
	buf := make([]byte, 4*len(v))
	for i, w := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(w))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func decodeFloats(s string, n int, what string) ([]float32, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("model: %s: %w", what, err)
	}
	if len(raw) != 4*n {
		return nil, fmt.Errorf("model: %d %s bytes, want %d", len(raw), what, 4*n)
	}
	out := make([]float32, n)
	for i := range out {
		w := math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
		if math.IsNaN(float64(w)) || math.IsInf(float64(w), 0) {
			return nil, fmt.Errorf("model: %s %d is not finite", what, i)
		}
		out[i] = w
	}
	return out, nil
}

// LoadModel parses and validates a serialised model. Every check here is
// what stands between a corrupted file and a silent 0.5 on every request.
func LoadModel(data []byte) (*Model, error) {
	var j modelJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("model: %w", err)
	}
	if j.Bits < minModelBits || j.Bits > maxModelBits {
		return nil, fmt.Errorf("model: bits %d out of [%d, %d]", j.Bits, minModelBits, maxModelBits)
	}
	if j.MinN < 1 || j.MaxN < j.MinN || j.MaxN > 8 {
		return nil, fmt.Errorf("model: n-gram range %d..%d invalid", j.MinN, j.MaxN)
	}
	m := &Model{Version: j.Version, Bits: j.Bits, MinN: j.MinN, MaxN: j.MaxN, Bias: j.Bias, Metrics: j.Metrics}
	if math.IsNaN(float64(m.Bias)) || math.IsInf(float64(m.Bias), 0) {
		return nil, fmt.Errorf("model: bias is not finite")
	}
	var err error
	if m.Weights, err = decodeFloats(j.Weights, 1<<j.Bits, "weights"); err != nil {
		return nil, err
	}
	if j.IDF != "" {
		if m.IDF, err = decodeFloats(j.IDF, 1<<j.Bits, "idf"); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// NewModel allocates an untrained model.
func NewModel(bits, minN, maxN int) *Model {
	return &Model{Bits: bits, MinN: minN, MaxN: maxN, Weights: make([]float32, 1<<bits)}
}

// Features returns the hashed feature indexes of a canonical text with
// their values. Three families of features share the space: character
// n-grams of the text padded with a space on each side, word unigrams and
// word bigrams. Words carry what an attack asks for ("ignore",
// "instructions", "restrictions"); character n-grams carry the spelling
// variants the words miss. A feature present several times counts once.
// Values are the feature's IDF, then the vector is L2-normalised.
func (m *Model) Features(canonical string) ([]uint32, []float32) {
	if canonical == "" {
		return nil, nil
	}
	mask := uint32(1<<m.Bits - 1)
	seen := map[uint32]struct{}{}
	var idx []uint32
	add := func(kind byte, key []byte) {
		h := fnv.New32a()
		h.Write([]byte{kind})
		h.Write(key)
		f := h.Sum32() & mask
		if _, dup := seen[f]; dup {
			return
		}
		seen[f] = struct{}{}
		idx = append(idx, f)
	}

	text := []rune(" " + canonical + " ")
	buf := make([]byte, 0, 4*m.MaxN)
	for n := m.MinN; n <= m.MaxN; n++ {
		for i := 0; i+n <= len(text); i++ {
			buf = buf[:0]
			for _, r := range text[i : i+n] {
				buf = binary.AppendUvarint(buf, uint64(r))
			}
			add('c', buf)
		}
	}
	words := strings.FieldsFunc(canonical, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
	for i, w := range words {
		add('w', []byte(w))
		if i > 0 {
			add('b', []byte(words[i-1]+" "+w))
		}
	}
	if len(idx) == 0 {
		return nil, nil
	}
	vals := make([]float32, len(idx))
	var norm float64
	for i, f := range idx {
		v := float32(1)
		if m.IDF != nil {
			v = m.IDF[f]
		}
		vals[i] = v
		norm += float64(v * v)
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range vals {
		vals[i] *= scale
	}
	return idx, vals
}

// Predict returns the probability that a canonical text is an attack.
func (m *Model) Predict(canonical string) float64 {
	idx, vals := m.Features(canonical)
	if len(idx) == 0 {
		return 0
	}
	z := m.Bias
	for i, f := range idx {
		z += m.Weights[f] * vals[i]
	}
	return sigmoid(float64(z))
}

func sigmoid(z float64) float64 {
	return 1 / (1 + math.Exp(-z))
}

// Example is a training sample: a canonical text and its label.
type Example struct {
	Canonical string
	Malicious bool
	// Weight scales the example's contribution, 1 by default. The trainer
	// uses it to balance classes.
	Weight float64
}

// TrainOptions drives Train.
type TrainOptions struct {
	Epochs       int
	LearningRate float64
	// L2 is the regularisation strength added to each gradient.
	L2 float64
	// Balance reweights the minority class so that both classes carry the
	// same total weight: a corpus with four attacks per honest request would
	// otherwise teach the model that everything is an attack.
	Balance bool
	Seed    int64
}

// DefaultTrainOptions is what the trainer ships with.
func DefaultTrainOptions() TrainOptions {
	return TrainOptions{Epochs: 10, LearningRate: 0.1, L2: 1e-4, Balance: true, Seed: 1}
}

// Train fits the model by stochastic gradient descent on the log loss. It
// first computes the IDF of every feature over the examples, then runs the
// epochs with a learning rate that decays each epoch. Examples are shuffled
// with the seed, so the same corpus and options give the same weights.
func (m *Model) Train(examples []Example, opts TrainOptions) {
	if opts.Epochs <= 0 {
		opts = DefaultTrainOptions()
	}
	rng := rand.New(rand.NewSource(opts.Seed))

	// Document frequencies, with the IDF reset so that Features returns
	// raw presence during this pass.
	m.IDF = nil
	df := make([]int32, 1<<m.Bits)
	feats := make([][]uint32, len(examples))
	for i, e := range examples {
		feats[i], _ = m.Features(e.Canonical)
		for _, f := range feats[i] {
			df[f]++
		}
	}
	n := float64(len(examples))
	m.IDF = make([]float32, 1<<m.Bits)
	for f, d := range df {
		m.IDF[f] = float32(math.Log((n+1)/(float64(d)+1)) + 1)
	}

	type prepared struct {
		idx  []uint32
		vals []float32
		y    float64
		w    float64
	}
	var pos, neg float64
	for _, e := range examples {
		if e.Malicious {
			pos++
		} else {
			neg++
		}
	}
	posW, negW := 1.0, 1.0
	if opts.Balance && pos > 0 && neg > 0 {
		if pos > neg {
			negW = pos / neg
		} else {
			posW = neg / pos
		}
	}
	data := make([]prepared, 0, len(examples))
	for _, e := range examples {
		idx, vals := m.Features(e.Canonical)
		if len(idx) == 0 {
			continue
		}
		w := e.Weight
		if w <= 0 {
			w = 1
		}
		y := 0.0
		if e.Malicious {
			y = 1
			w *= posW
		} else {
			w *= negW
		}
		data = append(data, prepared{idx, vals, y, w})
	}
	// AdaGrad: each feature has its own step, shrinking with the gradient
	// it has already received. A word seen in a dozen examples keeps a
	// large step and reaches a useful weight; a fragment seen everywhere
	// settles fast. Plain SGD does the opposite and learns the function
	// words of the benign corpus.
	acc := make([]float32, len(m.Weights))
	var biasAcc float32
	const eps = 1e-8
	for epoch := 0; epoch < opts.Epochs; epoch++ {
		rng.Shuffle(len(data), func(i, j int) { data[i], data[j] = data[j], data[i] })
		for _, d := range data {
			z := float64(m.Bias)
			for i, f := range d.idx {
				z += float64(m.Weights[f]) * float64(d.vals[i])
			}
			g := float32((sigmoid(z) - d.y) * d.w)
			biasAcc += g * g
			m.Bias -= float32(opts.LearningRate) * g / float32(math.Sqrt(float64(biasAcc)+eps))
			for i, f := range d.idx {
				gf := g*d.vals[i] + float32(opts.L2)*m.Weights[f]
				acc[f] += gf * gf
				m.Weights[f] -= float32(opts.LearningRate) * gf / float32(math.Sqrt(float64(acc[f])+eps))
			}
		}
	}
}

// Evaluate scores examples and returns precision, recall, F1 at a threshold
// plus the count of each outcome.
func (m *Model) Evaluate(examples []Example, threshold float64) Metrics {
	var mt Metrics
	for _, e := range examples {
		p := m.Predict(e.Canonical)
		mt.count(e.Malicious, p >= threshold)
	}
	mt.finish()
	return mt
}

// Metrics is a confusion matrix with its derived rates.
type Metrics struct {
	TP, FP, FN, TN        int
	Precision, Recall, F1 float64
}

func (mt *Metrics) count(malicious, flagged bool) {
	switch {
	case malicious && flagged:
		mt.TP++
	case malicious:
		mt.FN++
	case flagged:
		mt.FP++
	default:
		mt.TN++
	}
}

func (mt *Metrics) finish() {
	if mt.TP+mt.FP > 0 {
		mt.Precision = float64(mt.TP) / float64(mt.TP+mt.FP)
	}
	if mt.TP+mt.FN > 0 {
		mt.Recall = float64(mt.TP) / float64(mt.TP+mt.FN)
	}
	if mt.Precision+mt.Recall > 0 {
		mt.F1 = 2 * mt.Precision * mt.Recall / (mt.Precision + mt.Recall)
	}
}

func (mt Metrics) String() string {
	return fmt.Sprintf("P=%5.1f%% R=%5.1f%% F1=%5.1f%%  TP=%d FP=%d FN=%d TN=%d",
		mt.Precision*100, mt.Recall*100, mt.F1*100, mt.TP, mt.FP, mt.FN, mt.TN)
}

// TopFeatures is a debugging aid: the heaviest weights, positive first.
func (m *Model) TopFeatures(n int) (pos, neg []uint32) {
	idx := make([]uint32, len(m.Weights))
	for i := range idx {
		idx[i] = uint32(i)
	}
	sort.Slice(idx, func(i, j int) bool { return m.Weights[idx[i]] > m.Weights[idx[j]] })
	if n > len(idx) {
		n = len(idx)
	}
	pos = append(pos, idx[:n]...)
	for i := len(idx) - 1; i >= len(idx)-n; i-- {
		neg = append(neg, idx[i])
	}
	return pos, neg
}
