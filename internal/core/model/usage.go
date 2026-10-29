package model

import (
	"time"

	"github.com/rs/xid"
)

type UsageRecordID string

func NewUsageRecordID() UsageRecordID {
	return UsageRecordID(xid.New().String())
}

// CostSource identifies whether a UsageRecord's cost was reported by the
// provider itself or estimated from the configured per-model tariff.
type CostSource string

const (
	// CostSourceProvider means the cost was taken directly from the
	// provider's response (e.g. OpenRouter's usage.cost).
	CostSourceProvider CostSource = "provider"
	// CostSourceComputed means the cost was estimated from the model's
	// configured PromptCostPer1KTokens/CompletionCostPer1KTokens tariff,
	// because the provider did not report an actual cost.
	CostSourceComputed CostSource = "computed"
	// CostSourceEstimated means the token counts themselves are estimates, not
	// counts the provider published. A stream cut short before the provider
	// reported its usage leaves them unknown, and most providers only report in
	// the final chunk, which never arrives. The counts are then derived from the
	// request and from how much of the answer reached the client. Treat such a
	// record as an order of magnitude, not as a billing figure.
	CostSourceEstimated CostSource = "estimated"
)

// FeedsMonetaryBudget reports whether a usage record counts toward a monetary
// budget. Subscription-covered usage is governed by the subscription enforcer
// instead, a record with no cost moves no total, and one without an
// organization belongs to no budget at all.
//
// The store, the cache and the backfill must agree on this, or the totals they
// each produce drift apart. The backfill states the same rule in SQL, since it
// never sees a record as a value.
func FeedsMonetaryBudget(r UsageRecord) bool {
	return !r.PlanCovered() && r.Cost() != 0 && r.OrgID() != ""
}

// UsageStatus tells how the proxy call the record accounts for ended. A
// streamed answer can stop before the provider signals completion while the
// tokens already produced were billed and delivered, so the record exists
// either way and this is what sets it apart from a normal one.
type UsageStatus string

const (
	// UsageStatusOK means the call ran to completion.
	UsageStatusOK UsageStatus = "ok"
	// UsageStatusInterrupted means the provider failed mid-stream, after chunks
	// had already reached the client.
	UsageStatusInterrupted UsageStatus = "interrupted"
	// UsageStatusClientGone means the client hung up mid-stream — a closed tab,
	// an aborted request, a reverse proxy timing out.
	UsageStatusClientGone UsageStatus = "client_gone"
	// UsageStatusWriteFailed means writing the response failed for a reason
	// that is not the client going away. Unlike a hangup it is a server fault.
	UsageStatusWriteFailed UsageStatus = "write_failed"
	// UsageStatusTruncated means the provider closed the stream without
	// signalling completion and without reporting an error. What the exchange
	// cost is unknown; an upstream connection dropped cleanly looks like this.
	UsageStatusTruncated UsageStatus = "truncated"
)

// UsageRecord captures one proxy call with cost frozen at recording time.
type UsageRecord interface {
	WithID[UsageRecordID]

	UserID() UserID
	ApplicationID() ApplicationID
	OrgID() OrgID
	// TeamID is the team the request was attributed to, empty for a request
	// charged to the organization alone.
	TeamID() TeamID
	ProviderID() ProviderID
	ModelID() LLMModelID
	ProxyModelName() string
	AuthTokenID() string // empty = web session call
	PromptTokens() int
	CachedTokens() int
	CompletionTokens() int
	TotalTokens() int
	Cost() int64            // microcents, frozen at recording time (converted to org currency)
	Currency() string       // currency code, e.g. USD, EUR — frozen from provider at recording time
	CostSource() CostSource // whether Cost is provider-reported or computed from tariff
	// ResolvedModelName is the actual model used when a virtual model was resolved.
	// Empty if the requested model was not a virtual model.
	ResolvedModelName() string
	CreatedAt() time.Time
	// PlanCovered indicates that this request was served by a subscription provider.
	// Such records do not count toward monetary quotas.
	PlanCovered() bool
	// ProviderCost is the raw equivalent PAYG cost in the provider's own currency (microcents).
	// Used to measure rolling-window value budgets on subscription plans.
	ProviderCost() int64
	// ProviderKeyID is the API key of the provider the request went through,
	// PrimaryProviderKey for the provider's own.
	ProviderKeyID() ProviderKeyID
	// Status tells whether the call completed or was cut short. Interrupted
	// records count toward quotas and costs like any other: the tokens were
	// produced and billed.
	Status() UsageStatus
}

type BaseUsageRecord struct {
	id                UsageRecordID
	userID            UserID
	applicationID     ApplicationID
	orgID             OrgID
	teamID            TeamID
	providerID        ProviderID
	modelID           LLMModelID
	proxyModelName    string
	resolvedModelName string
	authTokenID       string
	promptTokens      int
	cachedTokens      int
	completionTokens  int
	totalTokens       int
	cost              int64
	currency          string
	costSource        CostSource
	createdAt         time.Time
	planCovered       bool
	providerCost      int64
	providerKeyID     ProviderKeyID
	status            UsageStatus
}

func (r *BaseUsageRecord) ID() UsageRecordID            { return r.id }
func (r *BaseUsageRecord) UserID() UserID               { return r.userID }
func (r *BaseUsageRecord) ApplicationID() ApplicationID { return r.applicationID }
func (r *BaseUsageRecord) OrgID() OrgID                 { return r.orgID }
func (r *BaseUsageRecord) TeamID() TeamID               { return r.teamID }
func (r *BaseUsageRecord) ProviderID() ProviderID       { return r.providerID }
func (r *BaseUsageRecord) ModelID() LLMModelID          { return r.modelID }
func (r *BaseUsageRecord) ProxyModelName() string       { return r.proxyModelName }
func (r *BaseUsageRecord) AuthTokenID() string          { return r.authTokenID }
func (r *BaseUsageRecord) PromptTokens() int            { return r.promptTokens }
func (r *BaseUsageRecord) CachedTokens() int            { return r.cachedTokens }
func (r *BaseUsageRecord) CompletionTokens() int        { return r.completionTokens }
func (r *BaseUsageRecord) TotalTokens() int             { return r.totalTokens }
func (r *BaseUsageRecord) Cost() int64                  { return r.cost }
func (r *BaseUsageRecord) Currency() string             { return r.currency }
func (r *BaseUsageRecord) CostSource() CostSource       { return r.costSource }
func (r *BaseUsageRecord) ResolvedModelName() string    { return r.resolvedModelName }
func (r *BaseUsageRecord) CreatedAt() time.Time         { return r.createdAt }

func (r *BaseUsageRecord) PlanCovered() bool            { return r.planCovered }
func (r *BaseUsageRecord) ProviderCost() int64          { return r.providerCost }
func (r *BaseUsageRecord) ProviderKeyID() ProviderKeyID { return r.providerKeyID }
func (r *BaseUsageRecord) Status() UsageStatus          { return r.status }

func (r *BaseUsageRecord) SetResolvedModelName(v string)    { r.resolvedModelName = v }
func (r *BaseUsageRecord) SetPlanCovered(v bool)            { r.planCovered = v }
func (r *BaseUsageRecord) SetProviderCost(v int64)          { r.providerCost = v }
func (r *BaseUsageRecord) SetStatus(v UsageStatus)          { r.status = v }
func (r *BaseUsageRecord) SetProviderKeyID(v ProviderKeyID) { r.providerKeyID = v }

var _ UsageRecord = &BaseUsageRecord{}

// NewUsageRecord builds the record of a request. teamID is the team the
// request is attributed to, empty for the organization alone: it feeds the
// team counters (see QuotaCounters).
func NewUsageRecord(
	userID UserID, applicationID ApplicationID, orgID OrgID, teamID TeamID, providerID ProviderID, modelID LLMModelID,
	proxyModelName, authTokenID string,
	promptTokens, cachedTokens, completionTokens int,
	cost int64,
	currency string,
	costSource CostSource,
	resolvedModelName string,
) *BaseUsageRecord {
	total := promptTokens + completionTokens
	return &BaseUsageRecord{
		id:                NewUsageRecordID(),
		userID:            userID,
		applicationID:     applicationID,
		orgID:             orgID,
		teamID:            teamID,
		providerID:        providerID,
		modelID:           modelID,
		proxyModelName:    proxyModelName,
		resolvedModelName: resolvedModelName,
		authTokenID:       authTokenID,
		promptTokens:      promptTokens,
		cachedTokens:      cachedTokens,
		completionTokens:  completionTokens,
		totalTokens:       total,
		cost:              cost,
		currency:          currency,
		costSource:        costSource,
		createdAt:         time.Now(),
		status:            UsageStatusOK,
	}
}

// QuotaCounter designates one running budget counter.
type QuotaCounter struct {
	Scope   QuotaScope
	ScopeID string
}

// QuotaCounters lists the budget counters a usage record feeds, in its
// organization: the organization always; the application when the call was
// made by one, else the user; and, when the call was attributed to a team, the
// team and, for a user, the user's counter inside the team. A record that
// feeds no monetary budget feeds no counter.
//
// The store, the cache and the backfill must agree on this list, or the totals
// they each produce drift apart.
//
// An application token authenticates through a shadow user, so a record can
// carry both: the shadow user has no budget of its own, and counting
// application spending on its counter would bypass the application budget
// (issue #64). For the same reason an application feeds no member counter of
// its team.
func QuotaCounters(r UsageRecord) []QuotaCounter {
	if !FeedsMonetaryBudget(r) {
		return nil
	}
	counters := []QuotaCounter{{QuotaScopeOrg, string(r.OrgID())}}
	appID, userID, teamID := r.ApplicationID(), r.UserID(), r.TeamID()
	switch {
	case appID != "":
		counters = append(counters, QuotaCounter{QuotaScopeApplication, string(appID)})
	case userID != "":
		counters = append(counters, QuotaCounter{QuotaScopeUser, string(userID)})
	}
	if teamID != "" {
		counters = append(counters, QuotaCounter{QuotaScopeTeam, string(teamID)})
		if appID == "" && userID != "" {
			counters = append(counters, QuotaCounter{QuotaScopeTeamMember, TeamMemberScopeID(teamID, userID)})
		}
	}
	return counters
}
