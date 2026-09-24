package passthrough

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// maxSniffedBody bounds what the sniffer retains while looking for usage
// counters. Relayed request bodies routinely reach hundreds of kilobytes;
// responses are far smaller, but the cap keeps a hostile or malformed upstream
// from growing the buffer without bound. Exceeding it costs the usage record
// for that call, never the response itself — bytes always reach the client.
const maxSniffedBody = 4 << 20 // 4 MiB

// TokenUsage holds the counters reported by an Anthropic-compatible response.
type TokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// Empty reports whether nothing usable was collected, in which case there is no
// usage worth recording.
func (u TokenUsage) Empty() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 &&
		u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0
}

// PromptTokens is the billable input side: the API reports cache reads and
// cache writes separately from input_tokens, so the total is their sum.
func (u TokenUsage) PromptTokens() int {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// CachedTokens is the share of the prompt served from the upstream cache, which
// is priced differently from a fresh prompt.
func (u TokenUsage) CachedTokens() int {
	return u.CacheReadInputTokens
}

// merge folds a newly seen usage object into the running total. Counters are
// replaced rather than summed: a streamed response restates the same totals
// across events (message_start carries the input side, message_delta the
// running output side), so summing would multiply them.
func (u *TokenUsage) merge(other TokenUsage) {
	if other.InputTokens > 0 {
		u.InputTokens = other.InputTokens
	}
	if other.OutputTokens > 0 {
		u.OutputTokens = other.OutputTokens
	}
	if other.CacheReadInputTokens > 0 {
		u.CacheReadInputTokens = other.CacheReadInputTokens
	}
	if other.CacheCreationInputTokens > 0 {
		u.CacheCreationInputTokens = other.CacheCreationInputTokens
	}
}

// sniffedEvent is the subset of an Anthropic response — streamed event or whole
// message — that carries usage. Both shapes are decoded with one struct: a
// message_start nests the counters under "message", every other shape puts them
// at the top level.
type sniffedEvent struct {
	Type    string      `json:"type"`
	Model   string      `json:"model"`
	Usage   *TokenUsage `json:"usage"`
	Message *struct {
		Model string      `json:"model"`
		Usage *TokenUsage `json:"usage"`
	} `json:"message"`
}

// usageSniffer wraps an upstream response body and extracts token usage as the
// bytes stream past, without buffering the response or delaying a single byte
// of it. onDone fires once, when the body is closed.
//
// It handles both response shapes the upstream produces: a text/event-stream of
// SSE events, and a single JSON message when the client asked for a
// non-streaming reply (Anthropic SDK clients retry that way after a stream
// fails, so a relay that only understood SSE would silently lose those calls).
type usageSniffer struct {
	upstream io.ReadCloser
	sse      bool
	onDone   func(usage TokenUsage, model string)

	buf      bytes.Buffer
	overflow bool
	usage    TokenUsage
	model    string
	done     bool
}

func newUsageSniffer(upstream io.ReadCloser, contentType string, onDone func(TokenUsage, string)) *usageSniffer {
	return &usageSniffer{
		upstream: upstream,
		sse:      strings.Contains(strings.ToLower(contentType), "text/event-stream"),
		onDone:   onDone,
	}
}

func (s *usageSniffer) Read(p []byte) (int, error) {
	n, err := s.upstream.Read(p)
	if n > 0 {
		s.consume(p[:n])
	}
	return n, err
}

func (s *usageSniffer) Close() error {
	if !s.done {
		s.done = true
		if !s.sse {
			// A non-streamed body is one JSON document: it can only be decoded
			// once complete.
			s.decodeInto(s.buf.Bytes())
		}
		if s.onDone != nil {
			s.onDone(s.usage, s.model)
		}
	}
	return s.upstream.Close()
}

func (s *usageSniffer) consume(chunk []byte) {
	if s.overflow {
		return
	}
	if s.buf.Len()+len(chunk) > maxSniffedBody {
		s.overflow = true
		s.buf.Reset()
		return
	}
	s.buf.Write(chunk)

	if !s.sse {
		return
	}

	// Decode every complete line and keep the trailing partial one. SSE data
	// lines are self-contained JSON, so a line is decodable as soon as it ends.
	for {
		idx := bytes.IndexByte(s.buf.Bytes(), '\n')
		if idx < 0 {
			return
		}
		line := make([]byte, idx)
		copy(line, s.buf.Bytes()[:idx])
		s.buf.Next(idx + 1)

		line = bytes.TrimSuffix(line, []byte("\r"))
		payload, ok := bytes.CutPrefix(line, []byte("data:"))
		if !ok {
			continue
		}
		s.decodeInto(bytes.TrimSpace(payload))
	}
}

func (s *usageSniffer) decodeInto(payload []byte) {
	if len(payload) == 0 || payload[0] != '{' {
		return
	}

	var event sniffedEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return
	}

	if event.Model != "" {
		s.model = event.Model
	}
	if event.Usage != nil {
		s.usage.merge(*event.Usage)
	}
	if event.Message != nil {
		if event.Message.Model != "" {
			s.model = event.Message.Model
		}
		if event.Message.Usage != nil {
			s.usage.merge(*event.Message.Usage)
		}
	}
}
