package proxy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

// toolCallClient answers with one tool call whose arguments still carry a
// placeholder, as a pseudonymizing node leaves them.
type toolCallClient struct {
	streamParts []string
}

func (c *toolCallClient) ChatCompletion(_ context.Context, _ ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	call := llm.NewToolCall("call_1", "Read", `{"path":"/home/PERSON_1/notes.md"}`)
	return llm.NewChatCompletionResponse(
		llm.NewMessage(llm.RoleAssistant, "Je lis le fichier de PERSON_1."),
		llm.NewChatCompletionUsage(0, 0, 0),
		call,
	), nil
}

func (c *toolCallClient) ChatCompletionStream(_ context.Context, _ ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, len(c.streamParts)+2)
	go func() {
		defer close(ch)
		ch <- llm.NewStreamChunk(llm.NewStreamDelta(llm.RoleAssistant, "Je lis."))
		for i, part := range c.streamParts {
			id, name := "", ""
			if i == 0 {
				id, name = "call_1", "Read"
			}
			ch <- llm.NewStreamChunk(llm.NewStreamDelta(llm.RoleAssistant, "", llm.NewToolCallDelta(0, id, name, part)))
		}
		ch <- llm.NewCompleteStreamChunk(llm.NewChatCompletionUsage(0, 0, 0))
	}()
	return ch, nil
}

func (c *toolCallClient) Embeddings(_ context.Context, _ []string, _ ...llm.EmbeddingsOptionFunc) (llm.EmbeddingsResponse, error) {
	return nil, nil
}

func (c *toolCallClient) Transcription(_ context.Context, _ []byte, _ ...llm.TranscriptionOptionFunc) (llm.TranscriptionResponse, error) {
	return nil, nil
}

var _ llm.Client = (*toolCallClient)(nil)

// Without this, the client runs the call against a path that does not exist.
func TestPipelineWrappedClient_RestoresPlaceholdersInResponseToolCalls(t *testing.T) {
	engine := newTestEngine("PERSON_1", "Jean Dupont")
	client := NewPipelineWrappedClient(&toolCallClient{}, engine, newTestForwardExecution(), pipeline.ExecutionContext{})

	resp, err := client.ChatCompletion(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	if calls[0].ID() != "call_1" || calls[0].Name() != "Read" {
		t.Errorf("id and name must survive: id=%q name=%q", calls[0].ID(), calls[0].Name())
	}

	args := toolCallArguments(calls[0].Parameters())
	var decoded map[string]any
	if err := json.Unmarshal([]byte(args), &decoded); err != nil {
		t.Fatalf("arguments are not valid JSON (%v): %q", err, args)
	}
	if decoded["path"] != "/home/Jean Dupont/notes.md" {
		t.Errorf("path = %v, want the restored value", decoded["path"])
	}

	// The response text is still restored too.
	if got := resp.Message().Content(); got != "Je lis le fichier de Jean Dupont." {
		t.Errorf("content = %q, want the restored text", got)
	}
}

func TestPipelineWrappedClient_RestoresPlaceholdersInStreamedToolCalls(t *testing.T) {
	engine := newTestEngine("PERSON_1", "Jean Dupont")
	inner := &toolCallClient{streamParts: []string{`{"path":"/home/`, `PERSON_1/notes.md"}`}}
	client := NewPipelineWrappedClient(inner, engine, newTestForwardExecution(), pipeline.ExecutionContext{})

	ch, err := client.ChatCompletionStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var args string
	var seen int
	for chunk := range ch {
		d := chunk.Delta()
		if d == nil {
			continue
		}
		for _, tc := range d.ToolCalls() {
			seen++
			args += tc.ParametersDelta()
		}
	}

	if seen != 1 {
		t.Errorf("tool call deltas emitted = %d, want the calls regrouped into one", seen)
	}
	if args != `{"path":"/home/Jean Dupont/notes.md"}` {
		t.Errorf("streamed arguments = %q, want the restored path", args)
	}
}

func TestEncodeToolCalls(t *testing.T) {
	if got := encodeToolCalls(nil); got != "" {
		t.Errorf("encodeToolCalls(nil) = %q, want empty", got)
	}

	got := encodeToolCalls([]llm.ToolCall{llm.NewToolCall("id_1", "Grep", `{"q":"x"}`)})
	if got != `[{"id":"id_1","name":"Grep","arguments":"{\"q\":\"x\"}"}]` {
		t.Errorf("encodeToolCalls = %s", got)
	}
}

func TestDecodeToolCalls(t *testing.T) {
	original := []llm.ToolCall{llm.NewToolCall("id_1", "Grep", `{"q":"PERSON_1"}`)}

	t.Run("nothing rewritten", func(t *testing.T) {
		if got := decodeToolCalls(context.Background(), "", original); got != nil {
			t.Errorf("decodeToolCalls = %#v, want nil", got)
		}
	})

	t.Run("malformed answer keeps the provider's calls", func(t *testing.T) {
		if got := decodeToolCalls(context.Background(), "not json", original); got != nil {
			t.Errorf("decodeToolCalls = %#v, want nil", got)
		}
	})

	t.Run("id and name are not taken from the node", func(t *testing.T) {
		got := decodeToolCalls(context.Background(), `[{"id":"id_1","name":"Forged","arguments":"{\"q\":\"Jean\"}"}]`, original)
		if len(got) != 1 {
			t.Fatalf("decodeToolCalls = %#v", got)
		}
		if got[0].Name() != "Grep" {
			t.Errorf("name = %q, want the provider's", got[0].Name())
		}
		if toolCallArguments(got[0].Parameters()) != `{"q":"Jean"}` {
			t.Errorf("arguments = %q, want the rewritten ones", toolCallArguments(got[0].Parameters()))
		}
	})
}
