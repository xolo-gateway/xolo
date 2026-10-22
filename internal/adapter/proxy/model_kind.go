package proxy

import (
	"context"
	"fmt"
	"net/http"

	"github.com/bornholm/genai/llm"
	"github.com/bornholm/genai/llm/provider"
	llmratelimit "github.com/bornholm/genai/llm/ratelimit"
	llmretry "github.com/bornholm/genai/llm/retry"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/pkg/errors"
	xologenai "github.com/xolo-gateway/xolo/internal/adapter/genai"
	"github.com/xolo-gateway/xolo/internal/core/model"

	_ "github.com/bornholm/genai/llm/provider/typesafe"
)

// requestKind is the kind of model a proxy request is for.
func requestKind(t genaiProxy.RequestType) model.ModelKind {
	switch t {
	case genaiProxy.RequestTypeDecision:
		return model.ModelKindDecision
	case genaiProxy.RequestTypeEmbedding:
		return model.ModelKindEmbeddings
	default:
		return model.ModelKindCompletion
	}
}

// kindRoutes names the routes a model of each kind answers.
var kindRoutes = map[model.ModelKind]string{
	model.ModelKindCompletion: "/chat/completions or /messages",
	model.ModelKindEmbeddings: "/embeddings",
	model.ModelKindDecision:   "/decisions",
}

// checkRequestKind refuses a request a model of another kind would have to
// answer: a chat completion sent to a decision model, say. The refusal is a
// 400, the client having picked the wrong model or route, and names both the
// route called and the one the model answers.
func checkRequestKind(req *genaiProxy.ProxyRequest, m model.LLMModel) error {
	asked := requestKind(req.Type)
	if m.Kind() == asked {
		return nil
	}
	return genaiProxy.NewBadRequestError(fmt.Sprintf("model '%s' is a %s model: it does not answer %s, call it on %s", req.Model, m.Kind(), kindRoutes[asked], kindRoutes[m.Kind()]))
}

// decisionClient builds the client of a decision model. It carries no chat
// client, and none of the chat-only wrappers: the extra body fields and the
// token limit apply to completions. The provider's rate limit, under its chat
// limit, its retries and the upstream timeout apply as to any model.
func (r *OrgModelRouter) decisionClient(ctx context.Context, m model.LLMModel, p model.Provider, name provider.Name, baseURL, apiKey string) (llm.Client, error) {
	opts := provider.NewDecisionProviderOptions(name)
	if opts == nil {
		// The same refusal as an embeddings model on an API without that
		// client: a 400 that says why, where genai would answer a plain
		// error with an opaque 500.
		return nil, genaiProxy.NewBadRequestError(fmt.Sprintf("model '%s' cannot answer decisions: API type '%s' has no decision client", m.ProxyName(), name))
	}
	xologenai.SetCommonOptions(opts, baseURL, apiKey, m.RealModel(), p.RequestHeaders())

	var client llm.Client
	client, err := provider.Create(ctx, func(o *provider.Options) error {
		o.Decision = &provider.ResolvedClientOptions{Provider: name, Specific: opts}
		return nil
	})
	if err != nil {
		return nil, errors.Wrapf(err, "could not create decision client for provider '%s'", p.Name())
	}

	if cfg := p.RateLimitConfig(); cfg != nil && cfg.Enabled {
		client = llmratelimit.NewClient(client, llmratelimit.WithChatLimit(cfg.Interval, cfg.MaxBurst))
	}
	if cfg := p.RetryConfig(); cfg != nil && cfg.Enabled {
		client = llmretry.NewClient(client, cfg.Delay, cfg.MaxAttempts)
	}
	return newTimeoutClient(client, r.upstreamTimeout), nil
}

// conversationKindError is the refusal of a pipeline or plugin naming a model
// that holds no conversation: a configuration mistake the pipeline answers
// with a 400, not a server error.
type conversationKindError struct {
	name string
	kind model.ModelKind
}

func (e *conversationKindError) Error() string {
	return fmt.Sprintf("model '%s' is a %s model and cannot serve a conversation", e.name, e.kind)
}

func (e *conversationKindError) Unwrap() error { return model.ErrNotAConversationModel }

// kindRefusal answers a request a model of another kind would have to take,
// with a 400 in the error format of the pipeline responses.
func kindRefusal(message, code string) *genaiProxy.HookResult {
	return &genaiProxy.HookResult{
		Response: &genaiProxy.ProxyResponse{
			StatusCode: http.StatusBadRequest,
			Body: map[string]any{"error": map[string]any{
				"type":    "invalid_request_error",
				"message": message,
				"code":    code,
			}},
		},
	}
}
