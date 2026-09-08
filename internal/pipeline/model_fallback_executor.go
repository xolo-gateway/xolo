package pipeline

import (
	"context"
	"log/slog"
	"sync"

	"github.com/bornholm/genai/llm"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// ModelFallbackExecutor handles NodeTypeModelFallback: an ordered list of real
// models where the next one is called when the previous fails. Resolution is
// eager for the first model, so a misconfigured primary fails at pipeline time
// like a plain model node, and lazy for the others, which are only looked up
// when they are needed.
//
// Candidates are real models of the org, not virtual models: a virtual model
// carries its own pipeline, whose post-response nodes could not run from
// inside a fallback chain.
type ModelFallbackExecutor struct {
	noopBackwardExecutor
	resolver ModelResolver
}

func NewModelFallbackExecutor(resolver ModelResolver) *ModelFallbackExecutor {
	return &ModelFallbackExecutor{resolver: resolver}
}

func (e *ModelFallbackExecutor) Forward(ctx context.Context, node model.PipelineNode, inputs map[string]interface{}, ec ExecutionContext) (*ForwardResult, error) {
	var data model.ModelFallbackNodeData
	if err := decodeNodeData(node, &data); err != nil {
		return nil, errors.Wrap(err, "model_fallback node: invalid data")
	}
	candidates := make([]string, 0, len(data.Models)+1)
	// A connected model_name port is tried first, ahead of the configured list.
	if name, ok := inputs["model_name"].(string); ok && name != "" {
		candidates = append(candidates, name)
	}
	for _, m := range data.Models {
		if m != "" {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return nil, errors.Errorf("model_fallback node %s: no model configured", node.ID)
	}

	orgID := model.OrgID(ec.OrgID)
	primary, realModel, modelID, err := e.resolver.ResolveRealModel(ctx, orgID, candidates[0])
	if err != nil {
		return nil, errors.Wrapf(err, "model_fallback node %s: could not resolve primary model %q", node.ID, candidates[0])
	}

	fc := &fallbackClient{
		candidates: candidates,
		resolve: func(ctx context.Context, name string) (llm.Client, string, model.LLMModelID, error) {
			return e.resolver.ResolveRealModel(ctx, orgID, name)
		},
	}
	fc.resolved = []resolvedCandidate{{client: primary, realModel: realModel, modelID: modelID}}

	return &ForwardResult{
		ResolvedClient:  fc,
		ResolvedModel:   realModel,
		ResolvedModelID: modelID,
		ModelOutcome:    fc,
		OutputValues:    map[string]interface{}{"response": ""},
	}, nil
}

type resolvedCandidate struct {
	client    llm.Client
	realModel string
	modelID   model.LLMModelID
}

// fallbackClient tries each candidate in turn. A candidate fails when its call
// returns an error; a cancelled context is not retried, since the caller has
// gone. Streaming is retried only while the stream has not started: once
// chunks have been forwarded, switching model would splice two answers.
type fallbackClient struct {
	candidates []string
	resolve    func(ctx context.Context, name string) (llm.Client, string, model.LLMModelID, error)

	mu       sync.Mutex
	resolved []resolvedCandidate
	used     *resolvedCandidate
}

// UsedModel implements UsedModelReporter.
func (c *fallbackClient) UsedModel() (string, model.LLMModelID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used == nil {
		return "", "", false
	}
	return c.used.realModel, c.used.modelID, true
}

func (c *fallbackClient) candidate(ctx context.Context, i int) (*resolvedCandidate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.resolved) <= i {
		name := c.candidates[len(c.resolved)]
		client, realModel, modelID, err := c.resolve(ctx, name)
		if err != nil {
			return nil, errors.Wrapf(err, "resolve fallback model %q", name)
		}
		c.resolved = append(c.resolved, resolvedCandidate{client: client, realModel: realModel, modelID: modelID})
	}
	return &c.resolved[i], nil
}

func (c *fallbackClient) markUsed(rc *resolvedCandidate) {
	c.mu.Lock()
	c.used = rc
	c.mu.Unlock()
}

func (c *fallbackClient) ChatCompletion(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	var lastErr error
	for i := range c.candidates {
		rc, err := c.candidate(ctx, i)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := rc.client.ChatCompletion(ctx, funcs...)
		if err == nil {
			c.markUsed(rc)
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		slog.WarnContext(ctx, "model fallback: candidate failed, trying next",
			slog.String("model", c.candidates[i]), slog.Any("error", err))
	}
	return nil, errors.Wrap(lastErr, "all fallback models failed")
}

func (c *fallbackClient) ChatCompletionStream(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	var lastErr error
	for i := range c.candidates {
		rc, err := c.candidate(ctx, i)
		if err != nil {
			lastErr = err
			continue
		}
		ch, err := rc.client.ChatCompletionStream(ctx, funcs...)
		if err == nil {
			c.markUsed(rc)
			return ch, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		slog.WarnContext(ctx, "model fallback: candidate failed to start stream, trying next",
			slog.String("model", c.candidates[i]), slog.Any("error", err))
	}
	return nil, errors.Wrap(lastErr, "all fallback models failed")
}

func (c *fallbackClient) Embeddings(ctx context.Context, input []string, funcs ...llm.EmbeddingsOptionFunc) (llm.EmbeddingsResponse, error) {
	rc, err := c.candidate(ctx, 0)
	if err != nil {
		return nil, err
	}
	c.markUsed(rc)
	return rc.client.Embeddings(ctx, input, funcs...)
}

func (c *fallbackClient) Transcription(ctx context.Context, audio []byte, funcs ...llm.TranscriptionOptionFunc) (llm.TranscriptionResponse, error) {
	rc, err := c.candidate(ctx, 0)
	if err != nil {
		return nil, err
	}
	c.markUsed(rc)
	return rc.client.Transcription(ctx, audio, funcs...)
}

var _ llm.Client = &fallbackClient{}
var _ UsedModelReporter = &fallbackClient{}
