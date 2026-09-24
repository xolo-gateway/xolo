package passthrough

import (
	"io"
	"strings"
	"testing"
)

// readAll drains a sniffer the way an HTTP client would, then closes it, and
// returns what the sniffer observed.
func readAll(t *testing.T, body, contentType string) (TokenUsage, string, string) {
	t.Helper()

	var (
		gotUsage TokenUsage
		gotModel string
	)
	sniffer := newUsageSniffer(io.NopCloser(strings.NewReader(body)), contentType, func(u TokenUsage, m string) {
		gotUsage = u
		gotModel = m
	})

	relayed, err := io.ReadAll(sniffer)
	if err != nil {
		t.Fatalf("could not read sniffer: %v", err)
	}
	if err := sniffer.Close(); err != nil {
		t.Fatalf("could not close sniffer: %v", err)
	}

	return gotUsage, gotModel, string(relayed)
}

func TestUsageSnifferStreamedResponse(t *testing.T) {
	// Shape of a real streamed reply: the input side lands on message_start,
	// the output side is restated on each message_delta.
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"claude-sonnet-5","usage":{"input_tokens":1200,"cache_read_input_tokens":800,"cache_creation_input_tokens":100,"output_tokens":1}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	usage, gotModel, relayed := readAll(t, body, "text/event-stream")

	if relayed != body {
		t.Error("sniffer altered the relayed bytes")
	}
	if gotModel != "claude-sonnet-5" {
		t.Errorf("model = %q, want claude-sonnet-5", gotModel)
	}
	if usage.InputTokens != 1200 {
		t.Errorf("InputTokens = %d, want 1200", usage.InputTokens)
	}
	if usage.OutputTokens != 42 {
		t.Errorf("OutputTokens = %d, want 42", usage.OutputTokens)
	}
	if got, want := usage.PromptTokens(), 2100; got != want {
		t.Errorf("PromptTokens() = %d, want %d", got, want)
	}
	if got, want := usage.CachedTokens(), 800; got != want {
		t.Errorf("CachedTokens() = %d, want %d", got, want)
	}
}

// A streamed reply restates the same counters across events. Summing them would
// bill the input side once per event, so the merge must replace, not add.
func TestUsageSnifferDoesNotAccumulateRestatedCounters(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"model":"m","usage":{"input_tokens":100}}}`,
		`data: {"type":"message_delta","usage":{"input_tokens":100,"output_tokens":10}}`,
		`data: {"type":"message_delta","usage":{"input_tokens":100,"output_tokens":25}}`,
		``,
	}, "\n")

	usage, _, _ := readAll(t, body, "text/event-stream")

	if usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100 (restated, not summed)", usage.InputTokens)
	}
	if usage.OutputTokens != 25 {
		t.Errorf("OutputTokens = %d, want 25 (last value wins)", usage.OutputTokens)
	}
}

// Anthropic SDK clients retry non-streaming after a stream fails, so a relay
// that only understood SSE would silently lose those calls.
func TestUsageSnifferNonStreamedResponse(t *testing.T) {
	body := `{"type":"message","model":"claude-opus-5","content":[{"type":"text","text":"hi"}],` +
		`"usage":{"input_tokens":7,"output_tokens":3,"cache_read_input_tokens":2}}`

	usage, gotModel, relayed := readAll(t, body, "application/json")

	if relayed != body {
		t.Error("sniffer altered the relayed bytes")
	}
	if gotModel != "claude-opus-5" {
		t.Errorf("model = %q, want claude-opus-5", gotModel)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 3 || usage.CacheReadInputTokens != 2 {
		t.Errorf("unexpected usage: %+v", usage)
	}
}

// A chunk boundary can fall anywhere, including mid-JSON. The sniffer must
// assemble lines across reads rather than decode whatever a single Read yields.
func TestUsageSnifferHandlesSplitChunks(t *testing.T) {
	full := `data: {"type":"message_delta","usage":{"output_tokens":99}}` + "\n"

	var got TokenUsage
	sniffer := newUsageSniffer(io.NopCloser(&chunkedReader{
		chunks: []string{full[:20], full[20:40], full[40:]},
	}), "text/event-stream", func(u TokenUsage, _ string) { got = u })

	if _, err := io.ReadAll(sniffer); err != nil {
		t.Fatalf("could not read sniffer: %v", err)
	}
	if err := sniffer.Close(); err != nil {
		t.Fatalf("could not close sniffer: %v", err)
	}

	if got.OutputTokens != 99 {
		t.Errorf("OutputTokens = %d, want 99", got.OutputTokens)
	}
}

func TestUsageSnifferIgnoresMalformedPayloads(t *testing.T) {
	body := strings.Join([]string{
		`data: not json at all`,
		`data: {"broken":`,
		`: a comment line`,
		`data: {"type":"message_delta","usage":{"output_tokens":5}}`,
		``,
	}, "\n")

	usage, _, relayed := readAll(t, body, "text/event-stream")

	if relayed != body {
		t.Error("sniffer altered the relayed bytes")
	}
	if usage.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5 — a malformed line must not abort sniffing", usage.OutputTokens)
	}
}

// Bytes must always reach the client, even when the response is too large to
// keep sniffing. Losing the usage record is the acceptable failure; truncating
// the response is not.
func TestUsageSnifferRelaysEverythingPastTheCap(t *testing.T) {
	body := strings.Repeat("x", maxSniffedBody+1024)

	usage, _, relayed := readAll(t, body, "application/json")

	if len(relayed) != len(body) {
		t.Errorf("relayed %d bytes, want %d", len(relayed), len(body))
	}
	if !usage.Empty() {
		t.Errorf("expected no usage past the cap, got %+v", usage)
	}
}

func TestTokenUsageEmpty(t *testing.T) {
	if !(TokenUsage{}).Empty() {
		t.Error("zero TokenUsage should be Empty")
	}
	if (TokenUsage{CacheReadInputTokens: 1}).Empty() {
		t.Error("cache-only usage should not be Empty")
	}
}

// chunkedReader hands out a fixed sequence of chunks, one per Read, to exercise
// boundary handling deterministically.
type chunkedReader struct {
	chunks []string
	i      int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.i])
	r.i++
	return n, nil
}
