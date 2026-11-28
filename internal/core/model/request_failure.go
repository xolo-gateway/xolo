package model

import (
	"time"

	"github.com/rs/xid"
)

type RequestFailureID string

func NewRequestFailureID() RequestFailureID {
	return RequestFailureID(xid.New().String())
}

// RequestFailure is a proxy call the provider failed before answering
// anything: an error, a refusal, a timeout. It has no usage record, since
// nothing was produced nor billed, and is kept apart from them so that no
// count of requests, budget or plan ever sees it. A stream that failed after
// its first chunk is not one: its tokens were delivered and billed, and its
// usage record says it was interrupted.
//
// The calls the gateway refused itself, for a quota or an access rule, are
// not recorded: they say nothing of the providers.
type RequestFailure struct {
	ID            RequestFailureID
	CreatedAt     time.Time
	UserID        UserID
	ApplicationID ApplicationID
	OrgID         OrgID
	// TeamID is the team the request was attributed to, empty when charged
	// to the organization alone.
	TeamID TeamID
	// ProviderID and ModelID designate the model the request was resolved
	// to, empty when it could not be read back.
	ProviderID ProviderID
	ModelID    LLMModelID
	// ProxyModelName is the model as the client asked for it.
	ProxyModelName string
	AuthTokenID    string
	// ProviderKeyID is the API key of the provider the request last went
	// through, PrimaryProviderKey for the provider's own.
	ProviderKeyID ProviderKeyID
	// HTTPStatus is the status the provider answered, zero when it did not
	// answer, and ErrorClass sorts the failure without its message, see
	// TraceSpan.ErrorClass.
	HTTPStatus int
	ErrorClass string
	Timing     UsageTiming
	Streamed   bool
	SessionID  string
	// Trace holds the spans of the call when they say more than the failure
	// does: several upstream attempts. Nil otherwise.
	Trace *UsageTrace
}

// NewRequestFailure returns a failure recorded now.
func NewRequestFailure() *RequestFailure {
	return &RequestFailure{ID: NewRequestFailureID(), CreatedAt: time.Now()}
}
