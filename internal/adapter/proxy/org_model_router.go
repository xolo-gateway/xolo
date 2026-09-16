package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/bornholm/genai/llm"
	"github.com/bornholm/genai/llm/provider"
	llmratelimit "github.com/bornholm/genai/llm/ratelimit"
	llmretry "github.com/bornholm/genai/llm/retry"
	"github.com/bornholm/genai/llm/tokenlimit"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/crypto"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/pkg/errors"

	_ "github.com/bornholm/genai/llm/provider/anthropic"
	_ "github.com/bornholm/genai/llm/provider/mistral"
	_ "github.com/bornholm/genai/llm/provider/openai"
	_ "github.com/bornholm/genai/llm/provider/openrouter"
)

// OrgModelRouter resolves the requested model name to an LLM client.
// Clients must use the qualified format "<org-slug>/<model-name>" to avoid
// ambiguity between models belonging to different organizations or having the
// same local name within an organization.
type OrgModelRouter struct {
	providerStore port.ProviderStore
	orgStore      port.OrgStore
	secretKey     string

	// clients caches the wrapped LLM clients per model so that stateful
	// wrappers (rate limiters, token buckets) are shared across requests
	// instead of being recreated — and thus reset — on every call.
	clientsMu sync.Mutex
	clients   *expirable.LRU[model.LLMModelID, *cachedClient]

	// upstreamTimeout bounds each provider call; zero leaves it unbounded.
	upstreamTimeout time.Duration
}

// cachedClient associates a built client with a fingerprint of the model and
// provider configuration it was built from; a stale fingerprint triggers a rebuild.
type cachedClient struct {
	client      llm.Client
	fingerprint string
}

// OrgModelRouterOption tunes an OrgModelRouter at construction.
type OrgModelRouterOption func(r *OrgModelRouter)

// WithUpstreamTimeout bounds every call to an upstream provider (see
// timeoutClient); zero disables the bound.
func WithUpstreamTimeout(timeout time.Duration) OrgModelRouterOption {
	return func(r *OrgModelRouter) {
		r.upstreamTimeout = timeout
	}
}

func NewOrgModelRouter(providerStore port.ProviderStore, orgStore port.OrgStore, secretKey string, funcs ...OrgModelRouterOption) *OrgModelRouter {
	r := &OrgModelRouter{
		providerStore: providerStore,
		orgStore:      orgStore,
		secretKey:     secretKey,
		clients:       expirable.NewLRU[model.LLMModelID, *cachedClient](256, nil, time.Hour),
	}
	for _, fn := range funcs {
		fn(r)
	}
	return r
}

func (r *OrgModelRouter) Name() string  { return "xolo.org-model-router" }
func (r *OrgModelRouter) Priority() int { return 10 }

// ResolveModel implements proxy.ModelResolverHook.
// The request model field must be in the format "<org-slug>/<model-name>".
func (r *OrgModelRouter) ResolveModel(ctx context.Context, req *genaiProxy.ProxyRequest) (llm.Client, string, error) {
	PopulateMetaFromContext(ctx, req.Metadata)

	tokenOrgID := OrgIDFromMeta(req.Metadata)
	if tokenOrgID == "" {
		return nil, "", errors.New("no org ID in request context; use an API key scoped to an organization")
	}

	orgSlug, proxyName, err := parseQualifiedModelName(req.Model)
	if err != nil {
		return nil, "", errors.Errorf("invalid model name %q: expected format \"<org-slug>/<model-name>\"", req.Model)
	}

	org, err := r.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			return nil, "", errors.Errorf("model '%s' not available in your organization", req.Model)
		}
		return nil, "", errors.WithStack(err)
	}

	// The token must be scoped to the same organization as the requested model.
	if org.ID() != tokenOrgID {
		return nil, "", errors.Errorf("model '%s' not available in your organization", req.Model)
	}

	llmModel, err := r.providerStore.GetLLMModelByProxyName(ctx, org.ID(), proxyName)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			return nil, "", errors.Errorf("model '%s' not available in your organization", req.Model)
		}
		return nil, "", errors.WithStack(err)
	}

	// Enforce RBAC: the user must be allowed to use this model, either through
	// the generic org-model-usage permission or an explicit model grant.
	perms, err := httpCtx.ResolvePermissions(ctx, org.ID())
	if err != nil {
		return nil, "", errors.WithStack(err)
	}
	if !perms.IsOwner() && !perms.Has(rbac.PermModelUseOrg) && !perms.HasModelAccess(string(llmModel.ID()), rbac.ModelKindLLM) {
		return nil, "", errors.Errorf("model '%s' not available in your organization", req.Model)
	}

	p, err := r.providerStore.GetProviderByID(ctx, llmModel.ProviderID())
	if err != nil {
		return nil, "", errors.WithStack(err)
	}

	// Verify capability for embedding requests
	if req.Type == genaiProxy.RequestTypeEmbedding {
		if !llmModel.Capabilities().Embeddings {
			return nil, "", errors.Errorf("model '%s' does not support embeddings", req.Model)
		}
		// The capability can be ticked on a model whose provider type has
		// no embeddings client (anthropic): say so instead of letting genai
		// fail with a generic error.
		if provider.NewEmbeddingsProviderOptions(provider.Name(p.Type())) == nil {
			return nil, "", errors.Errorf("model '%s' cannot serve embeddings: provider type '%s' has no embeddings client", req.Model, p.Type())
		}
	}

	// Store model ID in metadata for UsageTracker
	req.Metadata[MetaModelID] = string(llmModel.ID())

	client, err := r.clientForProvider(ctx, llmModel, p)
	if err != nil {
		return nil, "", errors.WithStack(err)
	}

	return client, llmModel.RealModel(), nil
}

// clientForModel returns the wrapped LLM client for the given model, building
// it on first use and caching it afterwards. The cache entry is invalidated
// whenever the model or its provider is updated (fingerprint mismatch) so that
// configuration changes are picked up without restarting the server.
func (r *OrgModelRouter) clientForModel(ctx context.Context, llmModel model.LLMModel) (llm.Client, error) {
	p, err := r.providerStore.GetProviderByID(ctx, llmModel.ProviderID())
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return r.clientForProvider(ctx, llmModel, p)
}

// clientForProvider is clientForModel for a caller that already holds the
// model's provider, sparing a second lookup per request.
func (r *OrgModelRouter) clientForProvider(ctx context.Context, llmModel model.LLMModel, p model.Provider) (llm.Client, error) {
	if !p.Active() {
		return nil, errors.Errorf("provider '%s' is not active", p.Name())
	}

	fingerprint := fmt.Sprintf("%d/%d", llmModel.UpdatedAt().UnixNano(), p.UpdatedAt().UnixNano())

	r.clientsMu.Lock()
	defer r.clientsMu.Unlock()

	if cached, ok := r.clients.Get(llmModel.ID()); ok && cached.fingerprint == fingerprint {
		return cached.client, nil
	}

	// Decrypt the API key
	decryptedKey, err := crypto.Decrypt(r.secretKey, p.APIKey())
	if err != nil {
		slog.ErrorContext(ctx, "could not decrypt provider API key", slog.Any("error", err))
		return nil, errors.New("provider configuration error")
	}

	providerName := provider.Name(p.Type())
	providerOpts := []provider.OptionFunc{
		withDynamicChatCompletion(providerName, p.BaseURL(), decryptedKey, llmModel.RealModel(), defaultMaxTokensFor(llmModel)),
	}
	if llmModel.Capabilities().Embeddings {
		// A provider without an embeddings client (anthropic) must not lose
		// its chat completions to a capability ticked on the model.
		if provider.NewEmbeddingsProviderOptions(providerName) == nil {
			slog.WarnContext(ctx, "model declares the embeddings capability but its provider type has no embeddings client",
				slog.String("model", string(llmModel.ID())), slog.String("providerType", p.Type()))
		} else {
			providerOpts = append(providerOpts, withDynamicEmbeddings(providerName, p.BaseURL(), decryptedKey, llmModel.RealModel()))
		}
	}

	client, err := provider.Create(ctx, providerOpts...)
	if err != nil {
		return nil, errors.Wrapf(err, "could not create LLM client for provider '%s'", p.Name())
	}

	// Inject the model's configured extra body fields (e.g. provider-specific
	// flags) into every request. Wrapped closest to the provider so the fields
	// are always present regardless of the resilience wrappers layered on top.
	client = newExtraFieldsClient(client, llmModel.ExtraBody())

	// Token limit (innermost — applied first, closest to the provider).
	// Always pass WithChatCompletionLimit explicitly to avoid the 500k TPM
	// silent default from tokenlimit.NewClient called bare.
	if cfg := llmModel.TokenLimitConfig(); cfg != nil && cfg.Enabled {
		client = tokenlimit.NewClient(client,
			tokenlimit.WithChatCompletionLimit(cfg.MaxTokens, cfg.Interval),
		)
	}

	// Rate limit wraps token limit: token-bucket (minInterval between requests,
	// burst = MaxBurst). The provider limit applies to both chat and embeddings;
	// without WithEmbeddingsLimit the embeddings limiter would silently default
	// to 1 req/s with burst 1.
	if cfg := p.RateLimitConfig(); cfg != nil && cfg.Enabled {
		client = llmratelimit.NewClient(client,
			llmratelimit.WithChatLimit(cfg.Interval, cfg.MaxBurst),
			llmratelimit.WithEmbeddingsLimit(cfg.Interval, cfg.MaxBurst),
		)
	}

	// Retry wraps everything (outermost): each retry attempt goes through
	// rate-limit and token-limit, preventing burst hammering during retries.
	if cfg := p.RetryConfig(); cfg != nil && cfg.Enabled {
		client = llmretry.NewClient(client, cfg.Delay, cfg.MaxAttempts)
	}

	// Timeout wraps the whole chain so the bound covers retries too and a
	// timeout is never retried.
	client = newTimeoutClient(client, r.upstreamTimeout)

	r.clients.Add(llmModel.ID(), &cachedClient{client: client, fingerprint: fingerprint})

	return client, nil
}

// ListModels implements proxy.ModelListerHook.
// Returns models in the qualified "<org-slug>/<model-name>" format.
func (r *OrgModelRouter) ListModels(ctx context.Context) ([]genaiProxy.ModelInfo, error) {
	orgID := OrgIDFromContext(ctx)
	if orgID == "" {
		return nil, nil
	}

	org, err := r.orgStore.GetOrgByID(ctx, model.OrgID(orgID))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	models, err := r.providerStore.ListEnabledLLMModels(ctx, model.OrgID(orgID))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	perms, err := httpCtx.ResolvePermissions(ctx, model.OrgID(orgID))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	infos := make([]genaiProxy.ModelInfo, 0, len(models))
	for _, m := range models {
		if !perms.IsOwner() && !perms.Has(rbac.PermModelUseOrg) && !perms.HasModelAccess(string(m.ID()), rbac.ModelKindLLM) {
			continue
		}
		infos = append(infos, genaiProxy.ModelInfo{
			ID:      org.Slug() + "/" + m.ProxyName(),
			OwnedBy: "xolo",
		})
	}
	return infos, nil
}

// ResolveRealModel implements pipeline.ModelResolver.
// proxyName may be local ("mistral-small") or qualified ("cadoles/mistral-small").
// When qualified, the org slug in the name takes precedence over the orgID parameter,
// allowing personal virtual models to reference models from any org by full name.
func (r *OrgModelRouter) ResolveRealModel(ctx context.Context, orgID model.OrgID, proxyName string) (llm.Client, string, model.LLMModelID, error) {
	if idx := strings.IndexByte(proxyName, '/'); idx > 0 {
		orgSlug := proxyName[:idx]
		proxyName = proxyName[idx+1:]
		if org, err := r.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug); err == nil {
			// When the referenced org differs from the token's org, verify that the
			// requesting user is actually a member of that org.
			if org.ID() != orgID {
				if err := r.assertOrgMembership(ctx, org.ID()); err != nil {
					return nil, "", "", errors.Errorf("model '%s/%s' not available in your organizations", orgSlug, proxyName)
				}
			}
			orgID = org.ID()
		}
	}

	llmModel, err := r.providerStore.GetLLMModelByProxyName(ctx, orgID, proxyName)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			return nil, "", "", errors.Errorf("model '%s' not available in organization", proxyName)
		}
		return nil, "", "", errors.WithStack(err)
	}

	client, err := r.clientForModel(ctx, llmModel)
	if err != nil {
		return nil, "", "", errors.WithStack(err)
	}

	return client, llmModel.RealModel(), llmModel.ID(), nil
}

// assertOrgMembership returns nil if the requesting user (from HTTP context) is
// a member of the given org, or an error otherwise.
func (r *OrgModelRouter) assertOrgMembership(ctx context.Context, orgID model.OrgID) error {
	u := httpCtx.User(ctx)
	if u == nil {
		return errors.New("no authenticated user in context")
	}
	memberships, err := r.orgStore.GetUserMemberships(ctx, u.ID())
	if err != nil {
		return errors.Wrap(err, "could not fetch memberships")
	}
	for _, m := range memberships {
		if m.OrgID() == orgID {
			return nil
		}
	}
	return errors.New("not a member of this organization")
}

// parseQualifiedModelName splits "org-slug/model-name" into its two parts.
func parseQualifiedModelName(name string) (orgSlug, proxyName string, err error) {
	idx := strings.IndexByte(name, '/')
	if idx <= 0 || idx == len(name)-1 {
		return "", "", errors.New("expected format \"<org-slug>/<model-name>\"")
	}
	return name[:idx], name[idx+1:], nil
}

var _ genaiProxy.ModelResolverHook = &OrgModelRouter{}
var _ genaiProxy.ModelListerHook = &OrgModelRouter{}

// withDynamicEmbeddings crée une OptionFunc d'embeddings pour un provider identifié à runtime.
func withDynamicEmbeddings(name provider.Name, baseURL, apiKey, model string) provider.OptionFunc {
	return func(o *provider.Options) error {
		opts := provider.NewEmbeddingsProviderOptions(name)
		if opts == nil {
			return errors.Errorf("unknown embeddings provider %q", name)
		}
		v := reflect.ValueOf(opts).Elem()
		if common := v.FieldByName("CommonOptions"); common.IsValid() {
			common.FieldByName("BaseURL").SetString(baseURL)
			common.FieldByName("APIKey").SetString(apiKey)
			common.FieldByName("Model").SetString(model)
		}
		o.Embeddings = &provider.ResolvedClientOptions{
			Provider: name,
			Specific: opts,
		}
		return nil
	}
}

// maxTokensProviders lists the provider types whose options carry a
// MaxTokens field meaning "default when the client sets none". The field is
// matched by name through reflection, so the allowlist keeps a future
// provider with a MaxTokens field of another meaning (a hard ceiling, say)
// from silently inheriting the output window.
var maxTokensProviders = map[provider.Name]bool{
	"anthropic": true,
}

// maxDefaultMaxTokens caps the max_tokens sent on behalf of a client that
// set none. The model's output window is the natural default, but on the
// Messages API it also sizes the reasoning budget (a share of max_tokens)
// and counts against the context window, so a 64k window would turn a
// request with no max_tokens into a 32k-token thinking budget and reject
// long prompts that used to pass. 16k keeps long answers possible without
// either effect getting out of hand; a client wanting more sets max_tokens.
const maxDefaultMaxTokens int64 = 16_384

// defaultMaxTokensFor returns the max_tokens to use when the client sets
// none: the model's output window, capped.
func defaultMaxTokensFor(m model.LLMModel) int64 {
	window := m.OutputWindow()
	if window <= 0 {
		return 0
	}
	return min(window, maxDefaultMaxTokens)
}

// withDynamicChatCompletion crée une OptionFunc pour un provider identifié à runtime.
// Tous les providers enregistrés embarquent provider.CommonOptions, on utilise
// la réflexion pour définir BaseURL, APIKey et Model sans type connu à la compilation.
//
// maxTokens renseigne le champ MaxTokens des providers listés dans
// maxTokensProviders (anthropic, dont l'API exige max_tokens) : c'est la
// fenêtre de sortie du modèle, à défaut de quoi le provider applique son
// propre défaut. Le champ est lu comme un défaut, jamais comme un plafond.
func withDynamicChatCompletion(name provider.Name, baseURL, apiKey, model string, maxTokens int64) provider.OptionFunc {
	return func(o *provider.Options) error {
		opts := provider.NewChatCompletionProviderOptions(name)
		if opts == nil {
			return errors.Errorf("unknown provider %q", name)
		}
		v := reflect.ValueOf(opts).Elem()
		if common := v.FieldByName("CommonOptions"); common.IsValid() {
			common.FieldByName("BaseURL").SetString(baseURL)
			common.FieldByName("APIKey").SetString(apiKey)
			common.FieldByName("Model").SetString(model)
		}
		if maxTokensProviders[name] && maxTokens > 0 {
			if field := v.FieldByName("MaxTokens"); field.IsValid() && field.CanSet() && field.Kind() == reflect.Int64 {
				field.SetInt(maxTokens)
			}
		}
		o.ChatCompletion = &provider.ResolvedClientOptions{
			Provider: name,
			Specific: opts,
		}
		return nil
	}
}
