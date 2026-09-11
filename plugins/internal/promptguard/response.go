package promptguard

import (
	"regexp"
	"sort"
	"strings"
)

// Response inspection is the output side of prompt-guard. It does NOT run the
// injection rules on the model's answer: an honest answer that discusses
// prompt injection would trip every one of them. It looks instead for the
// two things a compromised answer carries and an honest one does not, the
// output-side risks OWASP places at LLM02 (sensitive information disclosure)
// and in LLM01's rendered-output examples: a data-exfiltration channel in the
// rendered text (a link or Markdown image whose URL smuggles data out), and
// invisible or bidi characters that smuggle bytes past a human reader.

// ResponseFindingKind names what was found in a response.
type ResponseFindingKind string

const (
	FindingExfilURL      ResponseFindingKind = "exfil_url"      // link/image whose URL carries data
	FindingExternalImage ResponseFindingKind = "external_image" // Markdown image to an external host
	FindingInvisible     ResponseFindingKind = "invisible_characters"
	FindingBidi          ResponseFindingKind = "bidi_control"
)

// ResponseFinding is one hit in a response, with the byte span it covers so
// it can be redacted.
type ResponseFinding struct {
	Kind   ResponseFindingKind `json:"kind"`
	Weight float64             `json:"weight"`
	Start  int                 `json:"-"`
	End    int                 `json:"-"`
}

// ResponseResult is the outcome of InspectResponse.
type ResponseResult struct {
	// Risk is the noisy-OR of the findings, in [0, 1].
	Risk     float64           `json:"risk"`
	Findings []ResponseFinding `json:"findings,omitempty"`
	// InvisibleCount and ExfilURLs are surfaced for events.
	InvisibleCount int `json:"invisible_characters"`
	ExfilURLs      int `json:"exfil_urls"`
	// Canaries lists the configured canaries found in the answer, with the
	// form they took. Never their value.
	Canaries []CanaryHit `json:"canaries,omitempty"`
}

// ResponseOptions tunes InspectResponseWith.
type ResponseOptions struct {
	// MaxRunes bounds the text analysed (0 = DefaultMaxRunes).
	MaxRunes int
	// Canaries are strings that must never appear in an answer, in clear or
	// disguised. See canary.go.
	Canaries []string
}

// Kinds returns the distinct finding kinds, strongest first.
func (r ResponseResult) Kinds() []string {
	seen := map[ResponseFindingKind]float64{}
	for _, f := range r.Findings {
		if f.Weight > seen[f.Kind] {
			seen[f.Kind] = f.Weight
		}
	}
	kinds := make([]ResponseFindingKind, 0, len(seen))
	for k := range seen {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool {
		if seen[kinds[i]] == seen[kinds[j]] {
			return kinds[i] < kinds[j]
		}
		return seen[kinds[i]] > seen[kinds[j]]
	})
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = string(k)
	}
	return out
}

var (
	// mdLinkOrImageRe captures a Markdown link or image and its URL. Group 1
	// is "!" for an image, group 2 the URL.
	mdLinkOrImageRe = regexp.MustCompile(`(!?)\[[^\]]*\]\((https?://[^)\s]+)\)`)
	// dataParamRe recognises a query parameter whose name says it carries
	// content and whose value is long enough to be a payload.
	dataParamRe = regexp.MustCompile(`(?i)[?&](?:data|q|query|c|content|text|prompt|conversation|history|msg|message|payload|d|s|info|dump|exfil|leak|body|input|out|resp)=[^&\s]{12,}`)
	// opaqueBlobRe recognises a long opaque run (base64/hex-like) in a URL,
	// the shape of an encoded conversation smuggled into a path or value.
	opaqueBlobRe = regexp.MustCompile(`[A-Za-z0-9+/_-]{40,}={0,2}`)
)

// InspectResponse scores a model answer for output-side exfiltration and
// invisible-character smuggling. maxRunes <= 0 uses DefaultMaxRunes.
func InspectResponse(text string, maxRunes int) ResponseResult {
	return InspectResponseWith(text, ResponseOptions{MaxRunes: maxRunes})
}

// InspectResponseWith is InspectResponse with canaries.
func InspectResponseWith(text string, opts ResponseOptions) ResponseResult {
	maxRunes := opts.MaxRunes
	if maxRunes <= 0 {
		maxRunes = DefaultMaxRunes
	}
	text = strings.ToValidUTF8(text, "�")
	if utf8Runes := len([]rune(text)); utf8Runes > maxRunes {
		text = truncateRunes(text, maxRunes)
	}
	var res ResponseResult

	// Invisible and bidi characters: an honest answer has none. Bidi controls
	// are called out separately because they reorder displayed text.
	var invisible, bidi int
	for i, r := range text {
		if isBidiControl(r) {
			bidi++
			res.Findings = append(res.Findings, ResponseFinding{Kind: FindingBidi, Weight: 0.6, Start: i, End: i + len(string(r))})
		} else if isInvisible(r) {
			invisible++
			res.Findings = append(res.Findings, ResponseFinding{Kind: FindingInvisible, Weight: weightFor(invisible), Start: i, End: i + len(string(r))})
		}
	}
	res.InvisibleCount = invisible + bidi

	// Links and Markdown images whose URL carries data.
	for _, m := range mdLinkOrImageRe.FindAllStringSubmatchIndex(text, -1) {
		full := text[m[0]:m[1]]
		url := text[m[4]:m[5]]
		isImage := text[m[2]:m[3]] == "!"
		switch {
		case urlCarriesData(url):
			res.ExfilURLs++
			res.Findings = append(res.Findings, ResponseFinding{Kind: FindingExfilURL, Weight: 0.6, Start: m[0], End: m[1]})
		case isImage && isExternalAutoLoad(url):
			// An image auto-loads when rendered: even without an obvious data
			// param, an external image is a weak exfiltration signal (the
			// request itself leaks that the answer was read, and any path can
			// carry data). Weak on its own, meaningful with other signals.
			_ = full
			res.Findings = append(res.Findings, ResponseFinding{Kind: FindingExternalImage, Weight: 0.3, Start: m[0], End: m[1]})
		}
	}
	// Bare (non-Markdown) URLs carrying data, outside a Markdown link.
	for _, loc := range bareDataURLRe.FindAllStringIndex(text, -1) {
		if insideAny(loc[0], res.Findings) {
			continue
		}
		res.ExfilURLs++
		res.Findings = append(res.Findings, ResponseFinding{Kind: FindingExfilURL, Weight: 0.5, Start: loc[0], End: loc[1]})
	}

	// Planted secrets, whole or in pieces, in clear or disguised.
	if fs, hits := findCanaries(text, opts.Canaries); len(fs) > 0 {
		res.Findings = append(res.Findings, fs...)
		res.Canaries = hits
	}

	res.Risk = noisyOr(weights(res.Findings))
	return res
}

var bareDataURLRe = regexp.MustCompile(`https?://[^\s)>\]]*(?:[?&](?:data|q|query|content|text|prompt|conversation|history|payload|exfil|leak)=[^\s)>\]]{12,})`)

func weightFor(n int) float64 {
	if n >= 5 {
		return 0.6
	}
	return 0.5
}

func weights(fs []ResponseFinding) []float64 {
	// One weight per kind (the strongest), so ten invisible characters do not
	// stack into a false certainty.
	best := map[ResponseFindingKind]float64{}
	for _, f := range fs {
		if f.Weight > best[f.Kind] {
			best[f.Kind] = f.Weight
		}
	}
	out := make([]float64, 0, len(best))
	for _, w := range best {
		out = append(out, w)
	}
	return out
}

func insideAny(pos int, fs []ResponseFinding) bool {
	for _, f := range fs {
		if pos >= f.Start && pos < f.End {
			return true
		}
	}
	return false
}

// urlCarriesData reports whether a URL smuggles content: a data-named query
// parameter with a long value, or a long opaque base64/hex-like blob.
func urlCarriesData(url string) bool {
	if dataParamRe.MatchString(url) {
		return true
	}
	// An opaque blob in the query or a deep path segment, not the host.
	if i := strings.IndexAny(url, "?#"); i >= 0 {
		if opaqueBlobRe.MatchString(url[i:]) {
			return true
		}
	}
	if slash := strings.Index(url, "://"); slash >= 0 {
		rest := url[slash+3:]
		if p := strings.IndexByte(rest, '/'); p >= 0 {
			if opaqueBlobRe.MatchString(rest[p:]) {
				return true
			}
		}
	}
	return false
}

// isExternalAutoLoad reports a plain http(s) URL (every Markdown image is
// external here since it starts with http).
func isExternalAutoLoad(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}

// isBidiControl reports the bidirectional formatting characters used in
// Trojan-Source style reordering attacks.
func isBidiControl(r rune) bool {
	switch {
	case r >= 0x202A && r <= 0x202E, // embeddings and overrides
		r >= 0x2066 && r <= 0x2069, // isolates
		r == 0x061C:
		return true
	}
	return false
}

// Redact removes the smuggling from a response: invisible and bidi characters
// are stripped, and each flagged link or image span is replaced by a marker.
// It returns the cleaned text and whether anything changed.
func Redact(text string, res ResponseResult) (string, bool) {
	if len(res.Findings) == 0 {
		return text, false
	}
	// Replace URL, link and canary spans first, from the end so offsets stay
	// valid. A span nested in a larger one is dropped beforehand: a canary
	// smuggled inside an exfiltration URL goes with the URL.
	all := make([]ResponseFinding, 0, len(res.Findings))
	for _, f := range res.Findings {
		switch f.Kind {
		case FindingExfilURL, FindingExternalImage, FindingCanary, FindingCanaryFragment:
			all = append(all, f)
		}
	}
	spans := make([]ResponseFinding, 0, len(all))
	for i, f := range all {
		nested := false
		for j, o := range all {
			if i != j && o.Start <= f.Start && o.End >= f.End && (o.End-o.Start) > (f.End-f.Start) {
				nested = true
				break
			}
		}
		if !nested {
			spans = append(spans, f)
		}
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].Start == spans[j].Start {
			return spans[i].End > spans[j].End
		}
		return spans[i].Start > spans[j].Start
	})
	out := text
	limit := len(out)
	for _, f := range spans {
		if f.Start < 0 || f.End > limit || f.Start >= f.End {
			continue
		}
		placeholder := "[lien retiré]"
		if f.Kind == FindingCanary || f.Kind == FindingCanaryFragment {
			placeholder = "[donnée retirée]"
		}
		out = out[:f.Start] + placeholder + out[f.End:]
		limit = f.Start
	}
	// Strip invisible and bidi characters everywhere.
	out = strings.Map(func(r rune) rune {
		if isBidiControl(r) || isInvisible(r) {
			return -1
		}
		return r
	}, out)
	return out, out != text
}
