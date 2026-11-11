package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"time"

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
	resolver          ModelResolver
	drainTimeout      time.Duration
	firstChunkTimeout time.Duration
}

func NewModelFallbackExecutor(resolver ModelResolver) *ModelFallbackExecutor {
	return &ModelFallbackExecutor{
		resolver:     resolver,
		drainTimeout: 30 * time.Second,
	}
}

// WithDrainTimeout bounds draining abandoned streams. Zero or less means no limit.
func (e *ModelFallbackExecutor) WithDrainTimeout(timeout time.Duration) *ModelFallbackExecutor {
	e.drainTimeout = timeout
	return e
}

// WithFirstChunkTimeout bounds how long a fallback candidate may wait for its
// first stream chunk. Zero or less means no additional limit.
func (e *ModelFallbackExecutor) WithFirstChunkTimeout(timeout time.Duration) *ModelFallbackExecutor {
	e.firstChunkTimeout = timeout
	return e
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
	resolve := func(ctx context.Context, name string) (llm.Client, string, model.LLMModelID, error) {
		if err := ec.checkModelAccess(ctx, name); err != nil {
			return nil, "", "", err
		}
		return e.resolver.ResolveRealModel(ctx, orgID, ec.withoutTeam(name))
	}
	primary, realModel, modelID, err := resolve(ctx, candidates[0])
	if err != nil {
		return nil, errors.Wrapf(err, "model_fallback node %s: could not resolve primary model %q", node.ID, candidates[0])
	}

	fc := &fallbackClient{
		candidates:        candidates,
		resolve:           resolve,
		nodeID:            node.ID,
		drainTimeout:      e.drainTimeout,
		firstChunkTimeout: e.firstChunkTimeout,
		resolved:          map[int]*resolvedCandidate{0: {client: primary, realModel: realModel, modelID: modelID}},
	}

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
// returns an error or its stream closes or errors before its first chunk.
// Cancellation is not retried. The first non-error chunk commits the stream,
// including a preamble or usage-only chunk, to avoid splicing two answers.
// The first chunk has a per-candidate timeout when configured. Once a non-error
// chunk arrives, the stream continues under the request context. Cancellation
// is not retried. Usage on an abandoned error chunk is not forwarded or
// attributed to the successful model.
type fallbackClient struct {
	candidates        []string
	resolve           func(ctx context.Context, name string) (llm.Client, string, model.LLMModelID, error)
	nodeID            string
	drainTimeout      time.Duration
	firstChunkTimeout time.Duration

	mu       sync.Mutex
	resolved map[int]*resolvedCandidate
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
	if rc := c.resolved[i]; rc != nil {
		return rc, nil
	}
	name := c.candidates[i]
	client, realModel, modelID, err := c.resolve(ctx, name)
	if err != nil {
		return nil, errors.Wrapf(err, "resolve fallback model %q", name)
	}
	rc := &resolvedCandidate{client: client, realModel: realModel, modelID: modelID}
	c.resolved[i] = rc
	return rc, nil
}

func (c *fallbackClient) markUsed(rc *resolvedCandidate) {
	c.mu.Lock()
	c.used = rc
	c.mu.Unlock()
}

func (c *fallbackClient) ChatCompletion(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	var lastErr, resolveErr error
	for i := range c.candidates {
		if ctx.Err() != nil {
			break
		}
		rc, err := c.candidate(ctx, i)
		if err != nil {
			resolveErr = err
			if ctx.Err() != nil {
				break
			}
			slog.WarnContext(ctx, "model fallback: could not resolve candidate", slog.String("node", c.nodeID), slog.String("model", c.candidates[i]), slog.Any("error", err))
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
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if lastErr == nil {
		lastErr = resolveErr
	}
	return nil, errors.Wrap(lastErr, "all fallback models failed")
}

func (c *fallbackClient) ChatCompletionStream(ctx context.Context, funcs ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	var lastErr, resolveErr error
	for i := range c.candidates {
		if ctx.Err() != nil {
			break
		}
		rc, err := c.candidate(ctx, i)
		if err != nil {
			resolveErr = err
			if ctx.Err() != nil {
				break
			}
			slog.WarnContext(ctx, "model fallback: could not resolve candidate", slog.String("node", c.nodeID), slog.String("model", c.candidates[i]), slog.Any("error", err))
			continue
		}
		candidateCtx, cancel := context.WithCancel(ctx)
		ch, err := rc.client.ChatCompletionStream(candidateCtx, funcs...)
		if err == nil {
			var firstChunkTimer *time.Timer
			var firstChunkTimeout <-chan time.Time
			if c.firstChunkTimeout > 0 {
				firstChunkTimer = time.NewTimer(c.firstChunkTimeout)
				firstChunkTimeout = firstChunkTimer.C
			}
			select {
			case <-ctx.Done():
				cancel()
				if firstChunkTimer != nil {
					firstChunkTimer.Stop()
				}
				go llm.DrainStream(ch, c.drainTimeout)
				return nil, ctx.Err()
			case <-firstChunkTimeout:
				cancel()
				go llm.DrainStream(ch, c.drainTimeout)
				err = errors.Wrap(context.DeadlineExceeded, "timed out waiting for first chunk")
			case first, ok := <-ch:
				if !ok {
					err = errors.New("stream closed before the first chunk")
				} else if first.Error() == nil {
					c.markUsed(rc)
					out := make(chan llm.StreamChunk, 1)
					out <- first
					go func() {
						defer close(out)
						defer func() {
							cancel()
							llm.DrainStream(ch, c.drainTimeout)
						}()
						for {
							select {
							case <-ctx.Done():
								return
							case chunk, ok := <-ch:
								if !ok || !llm.SendChunk(ctx, out, chunk) {
									return
								}
							}
						}
					}()
					if firstChunkTimer != nil {
						firstChunkTimer.Stop()
					}
					return out, nil
				} else {
					err = first.Error()
					go llm.DrainStream(ch, c.drainTimeout)
				}
			}
			if firstChunkTimer != nil {
				firstChunkTimer.Stop()
			}
		}
		cancel()
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		slog.WarnContext(ctx, "model fallback: candidate failed to start stream, trying next",
			slog.String("model", c.candidates[i]), slog.Any("error", err))
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if lastErr == nil {
		lastErr = resolveErr
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
