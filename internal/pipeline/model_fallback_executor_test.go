package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// failingClient fails every call.
type failingClient struct{ dummyClient }

func (failingClient) ChatCompletion(context.Context, ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	return nil, errors.New("upstream down")
}

func (failingClient) ChatCompletionStream(context.Context, ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	return nil, errors.New("upstream down")
}

type fakeResolver struct {
	clients  map[string]llm.Client
	resolved []string
}

func (r *fakeResolver) ResolveRealModel(_ context.Context, _ model.OrgID, proxyName string) (llm.Client, string, model.LLMModelID, error) {
	r.resolved = append(r.resolved, proxyName)
	c, ok := r.clients[proxyName]
	if !ok {
		return nil, "", "", errors.New("unknown model " + proxyName)
	}
	return c, "real-" + proxyName, model.LLMModelID("id-" + proxyName), nil
}

func fallbackNode(models ...string) model.PipelineNode {
	data, _ := json.Marshal(model.ModelFallbackNodeData{Models: models})
	return model.PipelineNode{ID: "fb", Type: model.NodeTypeModelFallback, Data: data}
}

func TestModelFallback_PrimaryAnswers(t *testing.T) {
	r := &fakeResolver{clients: map[string]llm.Client{"a": newDummyClient("from a"), "b": newDummyClient("from b")}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "b"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ResolvedModel != "real-a" || res.ResolvedModelID != "id-a" {
		t.Errorf("primary must be reported as resolved model: %s %s", res.ResolvedModel, res.ResolvedModelID)
	}
	resp, err := res.ResolvedClient.ChatCompletion(context.Background())
	if err != nil || resp.Message().Content() != "from a" {
		t.Fatalf("expected the primary answer, got %v %v", resp, err)
	}
	if len(r.resolved) != 1 {
		t.Errorf("secondary must not be resolved when unused, resolved: %v", r.resolved)
	}
	if real, id, ok := res.ModelOutcome.UsedModel(); !ok || real != "real-a" || id != "id-a" {
		t.Errorf("used model: %s %s %v", real, id, ok)
	}
}

func TestModelFallback_SwitchesOnFailure(t *testing.T) {
	r := &fakeResolver{clients: map[string]llm.Client{"a": &failingClient{}, "b": &failingClient{}, "c": newDummyClient("from c")}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "b", "c"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := res.ResolvedClient.ChatCompletion(context.Background())
	if err != nil || resp.Message().Content() != "from c" {
		t.Fatalf("expected the third model to answer, got %v %v", resp, err)
	}
	if real, id, ok := res.ModelOutcome.UsedModel(); !ok || real != "real-c" || id != "id-c" {
		t.Errorf("usage must be attributed to the model that answered: %s %s %v", real, id, ok)
	}
	// Streaming follows the same chain.
	ch, err := res.ResolvedClient.ChatCompletionStream(context.Background())
	if err != nil || ch == nil {
		t.Fatalf("stream fallback: %v", err)
	}
}

func TestModelFallback_AllFail(t *testing.T) {
	r := &fakeResolver{clients: map[string]llm.Client{"a": &failingClient{}}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("a", "missing"), nil, ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := res.ResolvedClient.ChatCompletion(context.Background()); err == nil {
		t.Error("expected an error when every candidate fails")
	}
	if _, _, ok := res.ModelOutcome.UsedModel(); ok {
		t.Error("no model must be reported as used")
	}
}

func TestModelFallback_PortFirstAndValidation(t *testing.T) {
	r := &fakeResolver{clients: map[string]llm.Client{"port": newDummyClient("p"), "cfg": newDummyClient("c")}}
	res, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("cfg"), map[string]interface{}{"model_name": "port"}, ExecutionContext{})
	if err != nil || res.ResolvedModel != "real-port" {
		t.Errorf("connected model_name must come first: %v %v", res, err)
	}
	if _, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode(), nil, ExecutionContext{}); err == nil {
		t.Error("no model must fail")
	}
	if _, err := NewModelFallbackExecutor(r).Forward(context.Background(), fallbackNode("missing", "cfg"), nil, ExecutionContext{}); err == nil {
		t.Error("an unresolvable primary must fail at pipeline time")
	}
}
