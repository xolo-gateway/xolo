package passthrough

import (
	"context"
	"log/slog"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authn"
)

// identity is the caller a relayed call is attributed to, resolved from the
// same middleware chain that serves the rest of the API.
type identity struct {
	UserID        model.UserID
	OrgID         model.OrgID
	AuthTokenID   string
	ApplicationID model.ApplicationID
}

// identityFromContext mirrors proxy.XoloAuthExtractor: identity comes from the
// context populated by authn + bridge + memberships, never from a header read
// here. On the passthrough path Authorization has already been rewritten by
// CredentialSwap, so reading it directly would yield the wrong credential
// entirely.
func identityFromContext(ctx context.Context) (identity, bool) {
	user := httpx.User(ctx)
	if user == nil {
		return identity{}, false
	}

	id := identity{UserID: user.ID()}

	if authnUser := authnUserOrNil(ctx); authnUser != nil && authnUser.OrgID != "" {
		id.OrgID = model.OrgID(authnUser.OrgID)
		id.AuthTokenID = authnUser.TokenID
	} else if memberships := httpx.Memberships(ctx); len(memberships) > 0 {
		id.OrgID = memberships[0].OrgID()
	}

	if user.Provider() == model.ApplicationProvider {
		id.ApplicationID = model.ApplicationID(user.Subject())
	}

	if id.OrgID == "" {
		return identity{}, false
	}

	return id, true
}

// authnUserOrNil reads the authenticated identity without inheriting
// authn.ContextUser's panic when none is set. The relay always runs behind the
// authn middleware, so the value is normally present; a panic in the request
// path is still the wrong failure mode for a missing optional value.
func authnUserOrNil(ctx context.Context) (user *authn.User) {
	defer func() {
		if recover() != nil {
			user = nil
		}
	}()
	return authn.ContextUser(ctx)
}

// Recorder turns the usage counters observed on a relayed response into a
// UsageRecord, priced from the configured provider's tariff exactly as proxied
// traffic is. Relayed calls therefore land in the same ledger, quotas and
// dashboards as everything else — which is the point of the surface.
type Recorder struct {
	usageStore          port.UsageStore
	providerStore       port.ProviderStore
	orgStore            port.OrgStore
	exchangeRateService *service.ExchangeRateService
	providerID          model.ProviderID
}

func NewRecorder(
	usageStore port.UsageStore,
	providerStore port.ProviderStore,
	orgStore port.OrgStore,
	exchangeRateService *service.ExchangeRateService,
	providerID model.ProviderID,
) *Recorder {
	return &Recorder{
		usageStore:          usageStore,
		providerStore:       providerStore,
		orgStore:            orgStore,
		exchangeRateService: exchangeRateService,
		providerID:          providerID,
	}
}

// Record writes one usage record for a relayed call. It never returns an error:
// the response has already been streamed to the client by the time it runs, so
// a bookkeeping failure must be logged, not surfaced.
func (r *Recorder) Record(ctx context.Context, id identity, modelName string, usage TokenUsage) {
	if usage.Empty() {
		slog.DebugContext(ctx, "passthrough: no usage reported, nothing to record", slog.String("model", modelName))
		return
	}

	promptTokens := usage.PromptTokens()
	cachedTokens := usage.CachedTokens()
	completionTokens := usage.OutputTokens

	provider, err := r.providerStore.GetProviderByID(ctx, r.providerID)
	if err != nil {
		slog.ErrorContext(ctx, "passthrough: could not load provider, usage not recorded",
			slog.Any("error", errors.WithStack(err)), slog.String("providerID", string(r.providerID)))
		return
	}

	llmModel := r.resolveModel(ctx, id.OrgID, modelName)

	var (
		cost       int64
		costSource = model.CostSourceComputed
		modelID    model.LLMModelID
	)
	if llmModel != nil {
		modelID = llmModel.ID()
		nonCachedPrompt := promptTokens - cachedTokens
		cost = (int64(nonCachedPrompt) * llmModel.PromptCostPer1KTokens() / 1000) +
			(int64(cachedTokens) * llmModel.CachedPromptCostPer1KTokens() / 1000) +
			(int64(completionTokens) * llmModel.CompletionCostPer1KTokens() / 1000)
	} else {
		// The relay does not require every upstream model to be registered: an
		// unknown one is still metered in tokens, with a zero cost that is
		// visibly unpriced rather than silently wrong. Registering the model
		// under the passthrough provider is what turns cost on.
		slog.WarnContext(ctx, "passthrough: model not registered under provider, recording usage without cost",
			slog.String("model", modelName), slog.String("providerID", string(r.providerID)))
	}

	providerCost := cost
	providerCurrency := provider.Currency()
	recordCost := cost
	recordCurrency := providerCurrency

	org, err := r.orgStore.GetOrgByID(ctx, id.OrgID)
	if err != nil {
		slog.WarnContext(ctx, "passthrough: could not load org, keeping provider currency",
			slog.Any("error", errors.WithStack(err)), slog.String("orgID", string(id.OrgID)))
	} else {
		orgCurrency := org.Currency()
		if orgCurrency == "" {
			orgCurrency = model.DefaultCurrency
		}
		converted, convertErr := r.exchangeRateService.Convert(ctx, providerCost, providerCurrency, orgCurrency)
		if convertErr != nil {
			slog.WarnContext(ctx, "passthrough: currency conversion failed, keeping provider currency",
				slog.Any("error", convertErr))
		} else {
			recordCost = converted
			recordCurrency = orgCurrency
		}
	}

	record := model.NewUsageRecord(
		id.UserID,
		id.ApplicationID,
		id.OrgID,
		r.providerID,
		modelID,
		modelName,
		id.AuthTokenID,
		promptTokens,
		cachedTokens,
		completionTokens,
		recordCost,
		recordCurrency,
		costSource,
		modelName,
	)
	record.SetPlanCovered(provider.BillingMode() == model.BillingModeSubscription)
	record.SetProviderCost(providerCost)

	if err := r.usageStore.RecordUsage(ctx, record); err != nil {
		slog.ErrorContext(ctx, "passthrough: could not record usage", slog.Any("error", errors.WithStack(err)))
	}
}

// resolveModel finds the registered model a relayed call used. The name on the
// wire is the upstream one, so RealModel is matched first; ProxyName is tried
// as a fallback for the common case where an operator registered the model
// under its upstream name. Returns nil when nothing matches.
func (r *Recorder) resolveModel(ctx context.Context, orgID model.OrgID, modelName string) model.LLMModel {
	if modelName == "" {
		return nil
	}

	models, err := r.providerStore.ListLLMModels(ctx, orgID)
	if err != nil {
		slog.WarnContext(ctx, "passthrough: could not list models",
			slog.Any("error", errors.WithStack(err)), slog.String("orgID", string(orgID)))
		return nil
	}

	var byProxyName model.LLMModel
	for _, m := range models {
		if m.ProviderID() != r.providerID {
			continue
		}
		if m.RealModel() == modelName {
			return m
		}
		if byProxyName == nil && m.ProxyName() == modelName {
			byProxyName = m
		}
	}

	return byProxyName
}
