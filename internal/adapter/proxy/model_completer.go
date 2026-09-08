package proxy

import (
	"context"
	"strings"

	"github.com/bornholm/genai/llm"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/pipeline"
	"github.com/xolo-gateway/xolo/internal/plugin"
)

// Complete implements plugin.ModelCompleter: it resolves the requested model
// for the org (real or virtual, with the same recursion a model node applies)
// and runs a single non-streaming chat completion.
//
// The call is made on behalf of a plugin, outside of the proxied request's
// hook chain: it is therefore neither quota-checked nor recorded as usage.
// Callers should point it at cheap models.
func (a *PipelineHookAdapter) Complete(ctx context.Context, req plugin.ModelCompletionRequest) (*plugin.ModelCompletionResult, error) {
	if a.modelExecutor == nil {
		return nil, errors.New("model executor not configured")
	}

	ec := pipeline.ExecutionContext{
		OrgID:           string(req.OrgID),
		UserID:          string(req.UserID),
		VisitedVMs:      map[model.VirtualModelID]struct{}{},
		PersonalVMStore: a.personalVMStore,
	}

	client, realModel, err := a.modelExecutor.ResolveClient(ctx, req.ProxyName, ec)
	if err != nil {
		return nil, errors.Wrapf(err, "resolve model %q", req.ProxyName)
	}

	messages := make([]llm.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, llm.NewMessage(llm.Role(strings.ToLower(m.Role)), m.Content))
	}

	opts := []llm.ChatCompletionOptionFunc{llm.WithMessages(messages...)}
	if req.Temperature != nil {
		opts = append(opts, llm.WithTemperature(*req.Temperature))
	}
	if req.MaxTokens != nil {
		opts = append(opts, llm.WithMaxCompletionTokens(*req.MaxTokens))
	}
	if req.JSONResponse {
		opts = append(opts, llm.WithResponseFormat(llm.ResponseFormatJSON))
	}

	resp, err := client.ChatCompletion(ctx, opts...)
	if err != nil {
		return nil, errors.Wrapf(err, "chat completion on %q", req.ProxyName)
	}

	res := &plugin.ModelCompletionResult{ResolvedModel: realModel}
	if msg := resp.Message(); msg != nil {
		res.Content = msg.Content()
	}
	if usage := resp.Usage(); usage != nil {
		res.PromptTokens = usage.PromptTokens()
		res.CompletionTokens = usage.CompletionTokens()
	}
	return res, nil
}

var _ plugin.ModelCompleter = &PipelineHookAdapter{}
