package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

// RelayMessages implements llm.MessagesRelayClient, running the backward pass
// over the relayed events the way ChatCompletionStream runs it over chunks.
//
// The events stay the upstream's, so fields genai knows nothing about —
// Claude Code's safety verdicts on message_delta, for one — reach the client.
// When a node may rewrite the response, only the content block events are
// held back until message_delta, restored, and re-emitted; message_start and
// the pings go through at once, keeping the client's idle watchdog fed.
func (c *PipelineWrappedClient) RelayMessages(ctx context.Context, body []byte, header http.Header) (<-chan llm.StreamChunk, error) {
	stream, err := llm.RelayMessages(ctx, c.inner, body, header)
	if err != nil {
		return nil, err
	}
	if !c.engine.MayModifyResponse(ctx, c.forwardExec) {
		return c.streamPassthrough(ctx, stream), nil
	}
	return c.relayRestoring(ctx, stream), nil
}

// SupportsMessagesRelay reports on the wrapped client, see
// llm.SupportsMessagesRelay.
func (c *PipelineWrappedClient) SupportsMessagesRelay() bool {
	return llm.SupportsMessagesRelay(c.inner)
}

func (c *PipelineWrappedClient) relayRestoring(ctx context.Context, source <-chan llm.StreamChunk) <-chan llm.StreamChunk {
	out := make(chan llm.StreamChunk, 8)

	go func() {
		defer close(out)

		// The backward pass runs once per response, whatever happens, and on a
		// context of its own: a client that hung up must not cost the nodes
		// recording the exchange their record.
		backCtx := context.WithoutCancel(ctx)
		var held heldBlocks
		var lastTokens *pipeline.TokensUsed
		ran := false
		flush := func(hadError bool) []llm.StreamChunk {
			ran = true
			return c.restore(backCtx, &held, lastTokens, hadError)
		}
		send := func(chunk llm.StreamChunk) bool {
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				if !ran {
					flush(true)
				}
				return false
			}
		}
		sendAll := func(chunks []llm.StreamChunk) bool {
			for _, chunk := range chunks {
				if !send(chunk) {
					return false
				}
			}
			return true
		}

		for chunk := range source {
			if u := chunk.Usage(); u != nil {
				lastTokens = &pipeline.TokensUsed{Prompt: u.PromptTokens(), Completion: u.CompletionTokens()}
			}

			raw, isRaw := chunk.(llm.RawEventChunk)
			closes := !isRaw || chunk.Error() != nil
			if !closes {
				event := parseRelayedEvent(raw.RawEvent())
				switch {
				case event.Type == "ping":
					if !send(chunk) {
						return
					}
					continue
				case ran:
					// Past message_delta everything goes through as is.
				case strings.HasPrefix(event.Type, "content_block_"):
					held.add(raw, event)
					continue
				case event.Type == "message_delta" || event.Type == "message_stop":
					closes = true
				case !held.empty():
					// An event the relay does not know, amid the content: it
					// keeps its place rather than closing the content early.
					held.events = append(held.events, heldEvent{chunk: raw})
					continue
				}
			}

			if closes && !ran && !sendAll(flush(chunk.Error() != nil)) {
				return
			}
			if !send(chunk) {
				return
			}
		}

		// Cut short before message_delta: the client still gets what was
		// produced, restored.
		if !ran {
			sendAll(flush(true))
		}
	}()

	return out
}

// textBlockSeparator joins the text blocks of a response for the backward
// pass, which takes one text, so that the restored text can be split back
// into its blocks. An ASCII record separator: the model does not write it and
// the pseudonymizer does not touch it.
const textBlockSeparator = "\x1e"

// restore runs the backward pass over the held content blocks and returns
// them rewritten, emptying held.
//
// Each text block gets its restored text on its first text delta, its other
// text deltas dropped; each tool_use block its restored input on its first
// input delta. Server tool blocks (web search, MCP) are left alone, as the
// pseudonymizer leaves them in the history: restoring them would send the
// real values upstream on the next turn. Thinking blocks are never touched,
// their signature covers their bytes. Blocks come back untouched when the
// pass fails or changes nothing, and the text blocks too when a node changed
// the text in a way that no longer splits into the same blocks: the client
// then sees placeholders, the upstream never sees real values.
func (c *PipelineWrappedClient) restore(ctx context.Context, held *heldBlocks, tokens *pipeline.TokensUsed, hadError bool) []llm.StreamChunk {
	h := *held
	*held = heldBlocks{}

	texts := make([]string, len(h.texts))
	for i, b := range h.texts {
		texts[i] = b.String()
	}
	text := strings.Join(texts, textBlockSeparator)

	outcome, err := c.engine.RunBackwardWithToolCalls(ctx, c.forwardExec, text, h.calls.json(), tokens, hadError)
	if err != nil {
		slog.WarnContext(ctx, "pipeline backward pass (relay) failed", slog.Any("error", err))
		return chunksOf(h.events)
	}

	restoredTexts := texts
	if outcome.ResponseContent != text {
		if parts := strings.Split(outcome.ResponseContent, textBlockSeparator); len(parts) == len(texts) {
			restoredTexts = parts
		} else {
			slog.WarnContext(ctx, "pipeline: the restored text no longer splits into the response's text blocks, left as is",
				slog.Int("blocks", len(texts)), slog.Int("parts", len(parts)))
		}
	}
	restoredArgs := map[int]string{}
	for _, d := range h.calls.rewritten(ctx, outcome.ToolCallsJSON) {
		restoredArgs[d.Index()] = d.ParametersDelta()
	}

	out := make([]llm.StreamChunk, 0, len(h.events))
	placed := map[int]bool{}
	for _, e := range h.events {
		index := e.event.Index
		switch e.event.Delta.Type {
		case "text_delta":
			pos, ok := h.textPos[index]
			if !ok || restoredTexts[pos] == texts[pos] {
				break
			}
			if !placed[index] {
				placed[index] = true
				out = append(out, e.withDelta(ctx, "text", restoredTexts[pos]))
			}
			continue
		case "input_json_delta":
			args, ok := restoredArgs[index]
			if !ok || !h.toolUse[index] || args == h.calls.byIdx[index].Arguments {
				break
			}
			if !placed[index] {
				placed[index] = true
				out = append(out, e.withDelta(ctx, "partial_json", args))
			}
			continue
		}
		out = append(out, e.chunk)
	}
	return out
}

// heldBlocks is the content of a response held back for the backward pass.
type heldBlocks struct {
	events []heldEvent
	// texts holds the text of each text block, in stream order; textPos maps
	// a content block index to its position there.
	texts   []*strings.Builder
	textPos map[int]int
	// toolUse marks the client tool calls, the only blocks whose input is
	// restored.
	toolUse map[int]bool
	calls   streamedToolCalls
}

func (h *heldBlocks) empty() bool { return len(h.events) == 0 }

func (h *heldBlocks) add(chunk llm.RawEventChunk, event relayedEvent) {
	h.events = append(h.events, heldEvent{chunk: chunk, event: event})
	switch {
	case event.Type == "content_block_start" && event.ContentBlock.Type == "text":
		if h.textPos == nil {
			h.textPos = map[int]int{}
		}
		h.textPos[event.Index] = len(h.texts)
		h.texts = append(h.texts, &strings.Builder{})
	case event.Type == "content_block_start" && event.ContentBlock.Type == "tool_use":
		if h.toolUse == nil {
			h.toolUse = map[int]bool{}
		}
		h.toolUse[event.Index] = true
		h.calls.collect([]llm.ToolCallDelta{llm.NewToolCallDelta(event.Index, event.ContentBlock.ID, event.ContentBlock.Name, "")})
	case event.Delta.Type == "text_delta":
		if pos, ok := h.textPos[event.Index]; ok {
			h.texts[pos].WriteString(event.Delta.Text)
		}
	case event.Delta.Type == "input_json_delta" && h.toolUse[event.Index]:
		h.calls.collect([]llm.ToolCallDelta{llm.NewToolCallDelta(event.Index, "", "", event.Delta.PartialJSON)})
	}
}

type heldEvent struct {
	chunk llm.RawEventChunk
	event relayedEvent
}

// withDelta returns the event with one field of its delta replaced, or the
// event unchanged if it cannot be re-encoded.
func (h heldEvent) withDelta(ctx context.Context, field, value string) llm.StreamChunk {
	var data map[string]any
	if err := json.Unmarshal(h.event.data, &data); err != nil {
		return h.chunk
	}
	delta, _ := data["delta"].(map[string]any)
	if delta == nil {
		return h.chunk
	}
	delta[field] = value

	var buf bytes.Buffer
	buf.WriteString("event: " + h.event.Type + "\ndata: ")
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(data); err != nil {
		slog.WarnContext(ctx, "pipeline: could not re-encode a relayed event", slog.Any("error", err))
		return h.chunk
	}
	buf.WriteString("\n") // Encode ends the data line, the blank line ends the event
	return llm.NewRawEventChunk(buf.Bytes(), h.chunk.Usage(), h.chunk.IsComplete())
}

func chunksOf(events []heldEvent) []llm.StreamChunk {
	out := make([]llm.StreamChunk, len(events))
	for i, h := range events {
		out[i] = h.chunk
	}
	return out
}

// relayedEvent is what the restoration reads of an Anthropic stream event.
type relayedEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`

	data []byte
}

func parseRelayedEvent(event []byte) relayedEvent {
	var parsed relayedEvent
	for _, line := range bytes.Split(event, []byte("\n")) {
		if data, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r"), []byte("data:")); ok {
			parsed.data = bytes.TrimSpace(data)
			_ = json.Unmarshal(parsed.data, &parsed)
			break
		}
	}
	return parsed
}

// chunkText is the response text a chunk carries, whether it is a translated
// delta or a relayed text_delta event.
func chunkText(chunk llm.StreamChunk) string {
	if raw, ok := chunk.(llm.RawEventChunk); ok {
		if event := parseRelayedEvent(raw.RawEvent()); event.Delta.Type == "text_delta" {
			return event.Delta.Text
		}
		return ""
	}
	if d := chunk.Delta(); d != nil {
		return d.Content()
	}
	return ""
}

var _ llm.MessagesRelayClient = (*PipelineWrappedClient)(nil)
