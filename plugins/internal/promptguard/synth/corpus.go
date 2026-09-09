package synth

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/xolo-gateway/xolo/plugins/internal/promptguard"
)

// WriteJSONL writes samples one JSON object per line.
func WriteJSONL(w io.Writer, samples []Sample) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	for _, s := range samples {
		if err := enc.Encode(s); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// ReadJSONL reads a corpus. Blank lines are skipped; a malformed line is an
// error, because a silently dropped sample is a metric that lies.
func ReadJSONL(r io.Reader) ([]Sample, error) {
	var out []Sample
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var s Sample
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if s.Text == "" {
			return nil, fmt.Errorf("line %d: empty text", line)
		}
		if s.Source == "" {
			s.Source = string(promptguard.SegmentUser)
		}
		if s.Split == "" && s.Family != "" {
			s.Split = SplitOf(s.Family)
		}
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ReadJSONLFile is ReadJSONL on a path.
func ReadJSONLFile(path string) ([]Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadJSONL(f)
}

// ReadTexts reads the "text" field of a JSONL file whatever its other
// fields, which is how the benign corpus of text-classifier is reused.
func ReadTexts(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var row struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err == nil && row.Text != "" {
			out = append(out, row.Text)
		}
	}
	return out, sc.Err()
}

// Similarity is the Jaccard index of the word trigram sets of two texts,
// after lower-casing and slot removal. Two templates above 0.6 say the same
// thing with the same words and one of them is redundant.
func Similarity(a, b string) float64 {
	sa, sb := trigrams(a), trigrams(b)
	if len(sa) == 0 && len(sb) == 0 {
		return 1
	}
	inter := 0
	for k := range sa {
		if sb[k] {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func trigrams(s string) map[string]bool {
	s = slotRe.ReplaceAllString(strings.ToLower(s), " _ ")
	words := strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r > 127)
	})
	out := map[string]bool{}
	if len(words) < 3 {
		if len(words) > 0 {
			out[strings.Join(words, " ")] = true
		}
		return out
	}
	for i := 0; i+3 <= len(words); i++ {
		out[strings.Join(words[i:i+3], " ")] = true
	}
	return out
}

// MostSimilar returns the template of the set closest to body, and the
// similarity.
func MostSimilar(body string, templates []*Template) (*Template, float64) {
	var best *Template
	bestSim := 0.0
	for _, t := range templates {
		if sim := Similarity(body, t.Body); sim > bestSim || best == nil {
			best, bestSim = t, sim
		}
	}
	return best, bestSim
}
