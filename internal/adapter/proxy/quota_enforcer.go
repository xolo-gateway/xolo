package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
)

// quotaResolver is satisfied by both QuotaService (prod) and gorm.Store (tests).
type quotaResolver interface {
	ResolveEffectiveQuota(ctx context.Context, userID model.UserID, orgID model.OrgID) (*model.EffectiveQuota, error)
}

// XoloQuotaEnforcer is a PreRequestHook that checks the effective budget quota
// for the requesting user and org, rejecting requests that would exceed it.
type XoloQuotaEnforcer struct {
	quotaResolver quotaResolver   // for per-user effective quota
	quotaStore    port.QuotaStore // for org-level GetQuota + SumCost checks
	usageStore    port.UsageStore
	providerStore port.ProviderStore
}

func NewXoloQuotaEnforcer(quotaResolver quotaResolver, quotaStore port.QuotaStore, usageStore port.UsageStore, providerStore port.ProviderStore) *XoloQuotaEnforcer {
	return &XoloQuotaEnforcer{
		quotaResolver: quotaResolver,
		quotaStore:    quotaStore,
		usageStore:    usageStore,
		providerStore: providerStore,
	}
}

func (e *XoloQuotaEnforcer) Name() string  { return "xolo.quota-enforcer" }
func (e *XoloQuotaEnforcer) Priority() int { return 5 }

// PreRequest implements proxy.PreRequestHook.
func (e *XoloQuotaEnforcer) PreRequest(ctx context.Context, req *genaiProxy.ProxyRequest) (*genaiProxy.HookResult, error) {
	populateMetaFromContext(ctx, req)

	userID := model.UserID(req.UserID)
	orgID := OrgIDFromMeta(req.Metadata)

	if userID == "" || orgID == "" {
		// No auth context — let the request through (will fail at auth extractor level)
		return nil, nil
	}

	// Resolve the model (and its provider) once and reuse it for both skip checks
	// below, instead of looking each up independently. On the hot path these reads
	// are served by the provider store cache.
	llmModel, provider := e.resolveModelAndProvider(ctx, req)
	if llmModel != nil {
		// ── Skip quota check if model has zero cost ──────────────────────────────
		if llmModel.PromptCostPer1KTokens() == 0 && llmModel.CompletionCostPer1KTokens() == 0 {
			return nil, nil
		}

		// ── Skip quota check if model is subscription-billed (governed by subscription_enforcer) ──
		if provider != nil && provider.BillingMode() == model.BillingModeSubscription {
			return nil, nil
		}
	}

	// ── Per-user quota check (effective = min of user quota and org quota) ──────
	effectiveQuota, err := e.quotaResolver.ResolveEffectiveQuota(ctx, userID, orgID)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	now := time.Now()
	currency := effectiveQuota.Currency

	if effectiveQuota.DailyBudget != nil {
		spent, err := e.usageStore.SumQuotaCostSince(ctx, model.QuotaScopeUser, string(userID), orgID, model.StartOfDay(now))
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if spent >= *effectiveQuota.DailyBudget {
			return &genaiProxy.HookResult{
				Response: rateLimitResponse(fmt.Sprintf(
					"Daily budget exceeded: %s / %s",
					formatMicrocents(spent, currency), formatMicrocents(*effectiveQuota.DailyBudget, currency),
				)),
			}, nil
		}
	}

	if effectiveQuota.MonthlyBudget != nil {
		spent, err := e.usageStore.SumQuotaCostSince(ctx, model.QuotaScopeUser, string(userID), orgID, model.StartOfMonth(now))
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if spent >= *effectiveQuota.MonthlyBudget {
			return &genaiProxy.HookResult{
				Response: rateLimitResponse(fmt.Sprintf(
					"Monthly budget exceeded: %s / %s",
					formatMicrocents(spent, currency), formatMicrocents(*effectiveQuota.MonthlyBudget, currency),
				)),
			}, nil
		}
	}

	if effectiveQuota.YearlyBudget != nil {
		spent, err := e.usageStore.SumQuotaCostSince(ctx, model.QuotaScopeUser, string(userID), orgID, model.StartOfYear(now))
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if spent >= *effectiveQuota.YearlyBudget {
			return &genaiProxy.HookResult{
				Response: rateLimitResponse(fmt.Sprintf(
					"Yearly budget exceeded: %s / %s",
					formatMicrocents(spent, currency), formatMicrocents(*effectiveQuota.YearlyBudget, currency),
				)),
			}, nil
		}
	}

	// ── Org-wide quota check (total spending by all users in the org) ──────────
	orgQuota, err := e.quotaStore.GetQuota(ctx, model.QuotaScopeOrg, string(orgID))
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		return nil, errors.WithStack(err)
	}
	if orgQuota != nil {
		orgCurrency := orgQuota.Currency()

		if orgQuota.DailyBudget() != nil {
			orgSpent, err := e.sumOrgCost(ctx, orgID, model.StartOfDay(now))
			if err != nil {
				return nil, errors.WithStack(err)
			}
			if orgSpent >= *orgQuota.DailyBudget() {
				return &genaiProxy.HookResult{
					Response: rateLimitResponse(fmt.Sprintf(
						"Organization daily budget exceeded: %s / %s",
						formatMicrocents(orgSpent, orgCurrency), formatMicrocents(*orgQuota.DailyBudget(), orgCurrency),
					)),
				}, nil
			}
		}

		if orgQuota.MonthlyBudget() != nil {
			orgSpent, err := e.sumOrgCost(ctx, orgID, model.StartOfMonth(now))
			if err != nil {
				return nil, errors.WithStack(err)
			}
			if orgSpent >= *orgQuota.MonthlyBudget() {
				return &genaiProxy.HookResult{
					Response: rateLimitResponse(fmt.Sprintf(
						"Organization monthly budget exceeded: %s / %s",
						formatMicrocents(orgSpent, orgCurrency), formatMicrocents(*orgQuota.MonthlyBudget(), orgCurrency),
					)),
				}, nil
			}
		}

		if orgQuota.YearlyBudget() != nil {
			orgSpent, err := e.sumOrgCost(ctx, orgID, model.StartOfYear(now))
			if err != nil {
				return nil, errors.WithStack(err)
			}
			if orgSpent >= *orgQuota.YearlyBudget() {
				return &genaiProxy.HookResult{
					Response: rateLimitResponse(fmt.Sprintf(
						"Organization yearly budget exceeded: %s / %s",
						formatMicrocents(orgSpent, orgCurrency), formatMicrocents(*orgQuota.YearlyBudget(), orgCurrency),
					)),
				}, nil
			}
		}
	}

	return nil, nil
}

// sumOrgCost returns the total cost for all users in the org since the given time,
// summing across all stored currencies. Because records are converted to org currency
// at record time, this approximates the true total in org currency.
//
// The total is shared by every user of the org, and so is the counter it is read
// from: one lookup answers the check for all of them instead of one aggregation
// per request per user.
func (e *XoloQuotaEnforcer) sumOrgCost(ctx context.Context, orgID model.OrgID, since time.Time) (int64, error) {
	total, err := e.usageStore.SumQuotaCostSince(ctx, model.QuotaScopeOrg, string(orgID), orgID, since)
	if err != nil {
		return 0, errors.WithStack(err)
	}
	return total, nil
}

func rateLimitResponse(message string) *genaiProxy.ProxyResponse {
	return &genaiProxy.ProxyResponse{
		StatusCode: 429,
		Body: map[string]any{
			"error": map[string]any{
				"message": message,
				"type":    "rate_limit_error",
				"code":    "quota_exceeded",
			},
		},
	}
}

// formatMicrocents converts microcents to a currency string, e.g. 1000000 USD → "$1.00".
func formatMicrocents(v int64, currency string) string {
	symbols := map[string]string{
		"EUR": "€", "GBP": "£", "JPY": "¥", "CHF": "CHF ", "CAD": "CA$", "AUD": "A$",
	}
	symbol := "$"
	if s, ok := symbols[currency]; ok {
		symbol = s
	}
	return fmt.Sprintf("%.2f%s", float64(v)/1_000_000, symbol)
}

// resolveModelAndProvider resolves the requested LLM model and its provider for
// the pre-request skip checks (zero-cost, subscription). At PreRequest time
// ResolveModel has not yet run, so MetaModelID is usually unset and we fall back
// to a lookup by proxy name + orgID. A nil model means "could not resolve" — the
// caller then proceeds with the normal PAYG quota checks. The provider may be nil
// even when the model resolved (e.g. transient load error); callers must guard.
func (e *XoloQuotaEnforcer) resolveModelAndProvider(ctx context.Context, req *genaiProxy.ProxyRequest) (model.LLMModel, model.Provider) {
	var llmModel model.LLMModel

	if modelID := ModelIDFromMeta(req.Metadata); modelID != "" {
		m, err := e.providerStore.GetLLMModelByID(ctx, modelID)
		if err != nil {
			slog.DebugContext(ctx, "quota enforcer: could not load model by ID", slog.Any("error", err), slog.String("modelID", string(modelID)))
			return nil, nil
		}
		llmModel = m
	} else {
		orgID := OrgIDFromMeta(req.Metadata)
		if orgID == "" {
			return nil, nil
		}
		_, proxyName, err := parseQualifiedModelName(req.Model)
		if err != nil {
			return nil, nil
		}
		m, err := e.providerStore.GetLLMModelByProxyName(ctx, orgID, proxyName)
		if err != nil {
			slog.DebugContext(ctx, "quota enforcer: could not load model by proxy name", slog.Any("error", err), slog.String("model", req.Model))
			return nil, nil
		}
		llmModel = m
	}

	p, err := e.providerStore.GetProviderByID(ctx, llmModel.ProviderID())
	if err != nil {
		slog.DebugContext(ctx, "quota enforcer: could not load provider", slog.Any("error", err), slog.String("providerID", string(llmModel.ProviderID())))
		return llmModel, nil
	}
	return llmModel, p
}

var _ genaiProxy.PreRequestHook = &XoloQuotaEnforcer{}
