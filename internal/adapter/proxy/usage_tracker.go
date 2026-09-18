package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"strings"

	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/metrics"
)

// XoloUsageTracker is a PostResponseHook that records one UsageRecord per
// successful proxy call with cost frozen from the model's pricing, converted
// to the organization's base currency.
type XoloUsageTracker struct {
	usageStore          port.UsageStore
	providerStore       port.ProviderStore
	orgStore            port.OrgStore
	exchangeRateService *service.ExchangeRateService
}

func NewXoloUsageTracker(
	usageStore port.UsageStore,
	providerStore port.ProviderStore,
	orgStore port.OrgStore,
	exchangeRateService *service.ExchangeRateService,
) *XoloUsageTracker {
	return &XoloUsageTracker{
		usageStore:          usageStore,
		providerStore:       providerStore,
		orgStore:            orgStore,
		exchangeRateService: exchangeRateService,
	}
}

func (t *XoloUsageTracker) Name() string  { return "xolo.usage-tracker" }
func (t *XoloUsageTracker) Priority() int { return 100 }

// PostResponse implements proxy.PostResponseHook.
func (t *XoloUsageTracker) PostResponse(ctx context.Context, req *genaiProxy.ProxyRequest, res *genaiProxy.ProxyResponse) (*genaiProxy.HookResult, error) {
	if res.TokensUsed == nil || req.UserID == "" {
		return nil, nil
	}

	PopulateMetaFromContext(ctx, req.Metadata)

	orgID := OrgIDFromMeta(req.Metadata)
	if orgID == "" {
		return nil, nil
	}

	modelID := ModelIDFromMeta(req.Metadata)
	if modelID == "" {
		return nil, nil
	}

	// Get original and resolved model names from metadata.
	originalModel := req.Model
	resolvedModel := req.Model
	if v, ok := req.Metadata[MetaOriginalModel].(string); ok && v != "" {
		originalModel = v
	}
	if v, ok := req.Metadata[MetaResolvedModel].(string); ok && v != "" {
		resolvedModel = v
	}

	authTokenID := AuthTokenIDFromMeta(req.Metadata)
	applicationID := ApplicationIDFromMeta(req.Metadata)

	llmModel, err := t.providerStore.GetLLMModelByID(ctx, modelID)
	if err != nil {
		slog.ErrorContext(ctx, "usage tracker: could not load LLM model", slog.Any("error", err), slog.String("modelID", string(modelID)))
		return nil, nil
	}

	p, err := t.providerStore.GetProviderByID(ctx, llmModel.ProviderID())
	if err != nil {
		slog.ErrorContext(ctx, "usage tracker: could not load provider", slog.Any("error", err), slog.String("providerID", string(llmModel.ProviderID())))
		return nil, nil
	}

	promptTokens := res.TokensUsed.PromptTokens
	cachedTokens := res.TokensUsed.CachedTokens
	completionTokens := res.TokensUsed.CompletionTokens

	status := usageStatus(ctx, res)

	// The metric counts every interruption, whatever status it maps to: the
	// cause is already a label, and the point of the counter is to make the
	// interruption rate measurable.
	if res.Interruption != nil {
		metrics.StreamInterrupted.With(prometheus.Labels{
			metrics.LabelOrg:   string(orgID),
			metrics.LabelModel: llmModel.ProxyName(),
			metrics.LabelCause: string(res.Interruption.Cause),
		}).Inc()
	}

	// An interrupted stream whose provider never published its usage leaves
	// TokensUsed at zero, which means "unknown", not "free". Recording it as
	// free would hand the customer a free request and hide the provider's own
	// bill — the loss issue #43 exists to close. The counts are estimated
	// instead, and cost_source says so.
	estimatedUsage := false
	if res.Interruption != nil && !res.Interruption.PartialUsage {
		promptTokens, completionTokens = estimateInterruptedTokens(req, res.Interruption)
		cachedTokens = 0
		estimatedUsage = true
	}

	metrics.ChatCompletionRequests.With(prometheus.Labels{
		metrics.LabelOrg: string(orgID),
	}).Inc()

	metrics.CompletionTokens.With(prometheus.Labels{
		metrics.LabelOrg: string(orgID),
	}).Add(float64(completionTokens))

	metrics.PromptTokens.With(prometheus.Labels{
		metrics.LabelOrg: string(orgID),
	}).Add(float64(promptTokens))

	var (
		providerCost     int64
		providerCurrency string
		costSource       model.CostSource
	)
	if res.TokensUsed.Cost != nil && !estimatedUsage {
		// Provider reported the actual billed cost (e.g. OpenRouter usage.cost).
		providerCost = int64(math.Round(*res.TokensUsed.Cost * 1_000_000))
		providerCurrency = res.TokensUsed.CostCurrency
		costSource = model.CostSourceProvider
	} else {
		nonCachedPrompt := promptTokens - cachedTokens
		providerCost = (int64(nonCachedPrompt) * llmModel.PromptCostPer1KTokens() / 1000) +
			(int64(cachedTokens) * llmModel.CachedPromptCostPer1KTokens() / 1000) +
			(int64(completionTokens) * llmModel.CompletionCostPer1KTokens() / 1000)
		providerCurrency = p.Currency()
		costSource = model.CostSourceComputed
		if estimatedUsage {
			costSource = model.CostSourceEstimated
		}
	}

	planCovered := p.BillingMode() == model.BillingModeSubscription

	recordCost := providerCost
	recordCurrency := providerCurrency

	// Convert to org's base currency
	org, err := t.orgStore.GetOrgByID(ctx, orgID)
	if err != nil {
		slog.WarnContext(ctx, "usage tracker: could not load org, using provider currency", slog.Any("error", errors.WithStack(err)), slog.String("orgID", string(orgID)))
	} else {
		orgCurrency := org.Currency()
		if orgCurrency == "" {
			orgCurrency = model.DefaultCurrency
		}
		converted, convertErr := t.exchangeRateService.Convert(ctx, providerCost, providerCurrency, orgCurrency)
		if convertErr != nil {
			slog.WarnContext(ctx, "usage tracker: currency conversion failed, using provider currency", slog.Any("error", convertErr))
		} else {
			recordCost = converted
			recordCurrency = orgCurrency
		}

		// Display the org-facing model name (proxyName), not the raw name
		// forwarded to the upstream provider (llmModel.RealModel()), which is
		// what MetaResolvedModel carries after pipeline resolution.
		resolvedModel = org.Slug() + "/" + llmModel.ProxyName()
	}

	record := model.NewUsageRecord(
		model.UserID(req.UserID),
		applicationID,
		orgID,
		llmModel.ProviderID(),
		modelID,
		originalModel,
		authTokenID,
		promptTokens,
		cachedTokens,
		completionTokens,
		recordCost,
		recordCurrency,
		costSource,
		resolvedModel,
	)
	record.SetPlanCovered(planCovered)
	record.SetProviderCost(providerCost)
	record.SetStatus(status)

	if err := t.usageStore.RecordUsage(ctx, record); err != nil {
		slog.ErrorContext(ctx, "usage tracker: could not record usage", slog.Any("error", errors.WithStack(err)))
	}

	return nil, nil
}

var _ genaiProxy.PostResponseHook = &XoloUsageTracker{}

// usageStatus maps how the proxy reported the exchange ending onto the status
// stored on the record. A stream cut short still produced and delivered tokens
// the provider billed, so it is recorded like any other call; the status is
// what keeps it distinguishable afterwards.
//
// An unknown cause is recorded as interrupted rather than as a completed call:
// a cause added upstream is more likely to be another way a stream breaks than
// a new way for it to succeed, and a wrong "ok" is invisible.
func usageStatus(ctx context.Context, res *genaiProxy.ProxyResponse) model.UsageStatus {
	if res.Interruption == nil {
		return model.UsageStatusOK
	}
	switch res.Interruption.Cause {
	case genaiProxy.StreamInterruptionUpstream:
		return model.UsageStatusInterrupted
	case genaiProxy.StreamInterruptionClientGone:
		return model.UsageStatusClientGone
	case genaiProxy.StreamInterruptionWriteFailed:
		return model.UsageStatusWriteFailed
	case genaiProxy.StreamInterruptionTruncated:
		return model.UsageStatusTruncated
	default:
		slog.WarnContext(ctx, "usage tracker: unknown stream interruption cause, recording as interrupted",
			slog.String("cause", string(res.Interruption.Cause)))
		return model.UsageStatusInterrupted
	}
}

// charsPerToken is the ratio used to turn request text into a token count when
// no tokenizer is available. Tokenizers differ per model and none is shipped
// here; four characters per token is the usual rule of thumb for English prose
// and lands within a factor of two for the rest.
const charsPerToken = 4

// estimateInterruptedTokens derives token counts for a stream the provider
// never reported usage for.
//
// The prompt is whatever the client sent, and the provider charged it in full
// the moment it started generating, so it is estimated from the request text.
// The completion is only known through ChunksEmitted: an OpenAI-style stream
// carries roughly one token per content chunk, which is the closest measure of
// the volume produced that is always available.
//
// Both are approximations, and the record says so through
// model.CostSourceEstimated. Estimating beats recording a zero, which would
// assert that an answer the client received cost nothing.
func estimateInterruptedTokens(req *genaiProxy.ProxyRequest, interruption *genaiProxy.StreamInterruption) (promptTokens, completionTokens int) {
	return estimatePromptTokens(req.Body), interruption.ChunksEmitted
}

// estimatePromptTokens counts the text of the messages carried by an
// OpenAI-compatible request body. A body it cannot parse falls back to its
// whole length, which overestimates by the weight of the JSON scaffolding
// rather than reporting nothing.
func estimatePromptTokens(body []byte) int {
	if len(body) == 0 {
		return 0
	}

	var parsed struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Messages) == 0 {
		return len(body) / charsPerToken
	}

	chars := 0
	for _, message := range parsed.Messages {
		chars += len(messageText(message.Content))
	}
	return chars / charsPerToken
}

// messageText extracts the text of one message content, which the OpenAI
// schema allows to be either a plain string or a list of typed parts.
func messageText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}

	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text
	}

	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return string(content)
	}

	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(part.Text)
	}
	return builder.String()
}
