package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

// quotaResolver is satisfied by both QuotaService (prod) and gorm.Store (tests).
// ResolveEffectiveQuota merges user and org quotas (taking the minimum at each
// period); ResolveEffectiveQuotaForApplication merges application and org
// quotas the same way.
//
// Both methods also return the raw org quota the resolver loaded for the
// merge. The enforcer consumes it instead of issuing a second GetQuota on
// the hot path (issue #82).
//
// When MetaApplicationID is set, XoloQuotaEnforcer calls only
// ResolveEffectiveQuotaForApplication: the user-scope path is skipped because
// the shadow user's counter is never fed for an application record (see
// quotaUsageRows). The application budget is the only one the enforcer checks
// on the application path (issue #64).
//
// ResolveTeamQuotas returns the budgets of the team levels of a request
// attributed to a team: the team budget, and the member budget of the user
// inside it (nil for an application). Each is checked against its own counter.
type quotaResolver interface {
	ResolveEffectiveQuota(ctx context.Context, userID model.UserID, orgID model.OrgID) (*model.EffectiveQuota, model.Quota, error)
	ResolveEffectiveQuotaForApplication(ctx context.Context, appID model.ApplicationID, orgID model.OrgID) (*model.EffectiveQuota, model.Quota, error)
	ResolveTeamQuotas(ctx context.Context, teamID model.TeamID, userID model.UserID) (model.Quota, *model.EffectiveQuota, error)
}

// XoloQuotaEnforcer is a PreRequestHook that checks the effective budget quota
// for the requesting principal, rejecting requests that would exceed it.
//
// For an application token, the effective budget is the stricter of the
// application and organization budgets at each period; the shadow user's user
// counter is never fed and is not checked. For a regular user it is the
// stricter of the user and organization budgets. The org-wide branch then
// runs for both, against the organization counter, regardless of which scope
// drove the request.
type XoloQuotaEnforcer struct {
	quotaResolver quotaResolver
	usageStore    port.UsageStore
	providerStore port.ProviderStore
}

func NewXoloQuotaEnforcer(quotaResolver quotaResolver, usageStore port.UsageStore, providerStore port.ProviderStore) *XoloQuotaEnforcer {
	return &XoloQuotaEnforcer{
		quotaResolver: quotaResolver,
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

	// ── Skip quota check when the pipeline answers without any provider ────────
	// A terminal node that forges its own response (dummy-model) costs nothing:
	// charging it against a budget would turn a canned refusal into a 429.
	if pipelineAnsweredWithoutProvider(req) {
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

	now := time.Now()

	// ── Application quota check (effective = min of app quota and org quota) ────
	// An application authenticates through a shadow user whose UserID populates
	// req.UserID. Without this branch the only check the request sees is the
	// shadow user's budget — which is never set — so any application budget the
	// operator stored under QuotaScopeApplication would be ignored (issue #64).
	//
	// When appID is set the per-user check is skipped entirely rather than run
	// as a no-op. The shadow user's counter is never fed for application
	// records (quotaUsageRows), so the user-scope check could not fire anyway;
	// running it would still cost a GetOrgByID + ListOrgMembers when
	// ShareQuotaEqually is on, since the resolver would fall into the
	// share-by-members branch for a user with no personal quota. Skipping the
	// whole call is exact and cheaper.
	appID := ApplicationIDFromMeta(req.Metadata)
	if appID != "" {
		appQuota, orgQuota, err := e.quotaResolver.ResolveEffectiveQuotaForApplication(ctx, appID, orgID)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		if result, err := e.checkScope(ctx, model.QuotaScopeApplication, string(appID), orgID, now, appQuota, "Application"); err != nil {
			return nil, err
		} else if result != nil {
			return result, nil
		}

		if result, err := e.checkOrgWide(ctx, orgQuota, orgID, now); err != nil {
			return nil, err
		} else if result != nil {
			return result, nil
		}
	} else {
		// ── Per-user quota check (effective = min of user quota and org quota) ──────
		effectiveQuota, orgQuota, err := e.quotaResolver.ResolveEffectiveQuota(ctx, userID, orgID)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		if result, err := e.checkScope(ctx, model.QuotaScopeUser, string(userID), orgID, now, effectiveQuota, "User"); err != nil {
			return nil, err
		} else if result != nil {
			return result, nil
		}

		if result, err := e.checkOrgWide(ctx, orgQuota, orgID, now); err != nil {
			return nil, err
		} else if result != nil {
			return result, nil
		}
	}

	// ── Team levels, for a request attributed to a team ─────────────────────────
	// Every level on the request's path is checked against its own counter: an
	// organization or team budget would mean nothing otherwise.
	if teamID := TeamIDFromMeta(req.Metadata); teamID != "" {
		memberID := userID
		if appID != "" {
			// An application has no member budget in its team.
			memberID = ""
		}
		teamQuota, member, err := e.quotaResolver.ResolveTeamQuotas(ctx, teamID, memberID)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if teamQuota != nil {
			effective := &model.EffectiveQuota{
				Currency:      teamQuota.Currency(),
				DailyBudget:   teamQuota.DailyBudget(),
				MonthlyBudget: teamQuota.MonthlyBudget(),
				YearlyBudget:  teamQuota.YearlyBudget(),
			}
			if result, err := e.checkScope(ctx, model.QuotaScopeTeam, string(teamID), orgID, now, effective, "Team"); err != nil || result != nil {
				return result, err
			}
		}
		if member != nil {
			if result, err := e.checkScope(ctx, model.QuotaScopeTeamMember, model.TeamMemberScopeID(teamID, memberID), orgID, now, member, "Team member"); err != nil || result != nil {
				return result, err
			}
		}
	}

	return nil, nil
}

// checkOrgWide runs the three daily/monthly/yearly checks against the
// organization counter, using the raw org quota the resolver already loaded
// for the per-principal merge. Issue #82: the org-wide block used to call
// quotaStore.GetQuota here, duplicating the lookup the resolver did upstream.
// The contract is now on the resolver signature — every implementation must
// return the org quota as its second value, so a resolver that forgets the
// field does not compile. nil org quota means "none on file", same shape as
// a GetQuota that returned port.ErrNotFound. Returning a non-nil HookResult
// means a budget was exceeded and the caller must short-circuit.
func (e *XoloQuotaEnforcer) checkOrgWide(
	ctx context.Context,
	orgQuota model.Quota,
	orgID model.OrgID,
	now time.Time,
) (*genaiProxy.HookResult, error) {
	if orgQuota == nil {
		return nil, nil
	}

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

	return nil, nil
}

// periodCheck pairs the title-case label that goes into the rejection
// message with the calendar window start. Hoisting the titles out of the
// hot path keeps the loop allocation-free: a single runes-only lower-casing
// per call would otherwise run for every check.
type periodCheck struct {
	title   string
	sinceFn func(time.Time) time.Time
}

// quotaPeriods is the fixed order in which the three budgets are checked.
// Stable across versions: messages and wire format depend on it.
var quotaPeriods = []periodCheck{
	{"Daily", model.StartOfDay},
	{"Monthly", model.StartOfMonth},
	{"Yearly", model.StartOfYear},
}

// quotaMessageSubject joins a scope label with a period label to build the
// rejection message subject. Callers pass "User" or "Application" as the
// scope label and "Daily", "Monthly", "Yearly" as the period. The result is
// "User daily budget exceeded", "Application daily budget exceeded" — with
// the period lower-cased after the scope.
//
// The helper is defensive about its inputs:
//   - An empty subjectPrefix produces no leading space ("daily budget exceeded"
//     rather than " daily budget exceeded"), so a future caller that wants a
//     period-only message gets a sensible answer.
//   - An empty periodTitle drops the period entirely, so adding a new period
//     that forgets to set the title does not panic at runtime on the first
//     exceeding request; the resulting message is just "budget exceeded".
func quotaMessageSubject(subjectPrefix, periodTitle string) string {
	if periodTitle == "" {
		if subjectPrefix == "" {
			return "budget exceeded"
		}
		return subjectPrefix + " budget exceeded"
	}
	period := strings.ToLower(periodTitle[:1]) + periodTitle[1:]

	if subjectPrefix == "" {
		return period + " budget exceeded"
	}
	return subjectPrefix + " " + period + " budget exceeded"
}

// checkScope runs the three daily/monthly/yearly checks against the counter
// for one principal (user or application). subjectPrefix goes into the
// rejection message ("User", "Application" or empty for org-wide, which has
// its own hard-coded messages above); the helper joins it with the period
// label so callers cannot drop a space by accident.
//
// The quota argument is never nil in practice: both resolvers always allocate
// a non-nil EffectiveQuota. Returning a non-nil HookResult means a budget
// was exceeded and the caller must short-circuit.
func (e *XoloQuotaEnforcer) checkScope(
	ctx context.Context,
	scope model.QuotaScope,
	scopeID string,
	orgID model.OrgID,
	now time.Time,
	quota *model.EffectiveQuota,
	subjectPrefix string,
) (*genaiProxy.HookResult, error) {
	currency := quota.Currency

	budgets := []*int64{quota.DailyBudget, quota.MonthlyBudget, quota.YearlyBudget}

	for i, check := range quotaPeriods {
		budget := budgets[i]
		if budget == nil {
			continue
		}
		spent, err := e.usageStore.SumQuotaCostSince(ctx, scope, scopeID, orgID, check.sinceFn(now))
		if err != nil {
			return nil, errors.WithStack(err)
		}
		if spent >= *budget {
			return &genaiProxy.HookResult{
				Response: rateLimitResponse(fmt.Sprintf(
					"%s: %s / %s",
					quotaMessageSubject(subjectPrefix, check.title),
					formatMicrocents(spent, currency), formatMicrocents(*budget, currency),
				)),
			}, nil
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

// pipelineAnsweredWithoutProvider reports whether a pipeline forward pass has
// already resolved a client that reaches no provider. Model nodes always set
// ResolvedModelID, so an execution holding a client without one comes from a
// node that produced the answer itself.
func pipelineAnsweredWithoutProvider(req *genaiProxy.ProxyRequest) bool {
	forwardExec, ok := req.Metadata[metaPipelineExecution].(*pipeline.ForwardExecution)
	if !ok || forwardExec == nil || forwardExec.ResolvedClient == nil {
		return false
	}
	return forwardExec.ResolvedModelID == ""
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
