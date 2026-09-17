package proxy

import (
	"context"
	"log/slog"
	"math"

	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/metrics"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
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

	status := usageStatus(res)
	if status != model.UsageStatusOK {
		metrics.StreamInterrupted.With(prometheus.Labels{
			metrics.LabelOrg:   string(orgID),
			metrics.LabelModel: llmModel.ProxyName(),
			metrics.LabelCause: string(res.Interruption.Cause),
		}).Inc()
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
	if res.TokensUsed.Cost != nil {
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
func usageStatus(res *genaiProxy.ProxyResponse) model.UsageStatus {
	if res.Interruption == nil {
		return model.UsageStatusOK
	}
	switch res.Interruption.Cause {
	case genaiProxy.StreamInterruptionUpstream:
		return model.UsageStatusInterrupted
	case genaiProxy.StreamInterruptionClientGone:
		return model.UsageStatusClientGone
	default:
		return model.UsageStatusOK
	}
}
