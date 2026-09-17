package proxy

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// maxErrorAttributeLength caps the error text stored on a failed-request
// event: provider bodies can be long and the attribute is meant for triage.
const maxErrorAttributeLength = 500

// XoloEventEmitterHook records proxy calls in the event system: a
// "proxy.request" event for every successful call and a
// "proxy.request.failed" event for every call that ended with an error, so
// failures stay visible after the container logs have rotated.
type XoloEventEmitterHook struct {
	emitter port.EventEmitter
}

func NewXoloEventEmitterHook(emitter port.EventEmitter) *XoloEventEmitterHook {
	return &XoloEventEmitterHook{emitter: emitter}
}

func (h *XoloEventEmitterHook) Name() string  { return "xolo.event-emitter" }
func (h *XoloEventEmitterHook) Priority() int { return 110 }

func (h *XoloEventEmitterHook) baseAttributes(ctx context.Context, req *genaiProxy.ProxyRequest) (model.OrgID, map[string]string) {
	PopulateMetaFromContext(ctx, req.Metadata)

	attrs := map[string]string{
		"model": req.Model,
	}
	if id := AuthTokenIDFromMeta(req.Metadata); id != "" {
		attrs["auth_token_id"] = id
	}

	return OrgIDFromMeta(req.Metadata), attrs
}

// PostResponse implements proxy.PostResponseHook.
func (h *XoloEventEmitterHook) PostResponse(ctx context.Context, req *genaiProxy.ProxyRequest, res *genaiProxy.ProxyResponse) (*genaiProxy.HookResult, error) {
	if req.UserID == "" {
		return nil, nil
	}

	orgID, attrs := h.baseAttributes(ctx, req)

	if res.TokensUsed != nil {
		attrs["prompt_tokens"] = strconv.Itoa(res.TokensUsed.PromptTokens)
		attrs["completion_tokens"] = strconv.Itoa(res.TokensUsed.CompletionTokens)
		attrs["total_tokens"] = strconv.Itoa(res.TokensUsed.PromptTokens + res.TokensUsed.CompletionTokens)
	}

	// The request happened and produced usage whatever way the stream ended, so
	// proxy.request is emitted in every case. Anything counting proxy calls
	// would otherwise lose the interrupted ones — including client hangups and
	// truncated streams, which were counted as ordinary requests before
	// interruptions were reported at all.
	event := model.NewEvent(model.EventSourcePlatform, model.EventTypeProxyRequest,
		model.WithEventOrg(orgID),
		model.WithEventUser(model.UserID(req.UserID)),
		model.WithEventSeverity(model.SeverityInfo),
		model.WithEventMessage("Requête proxy: "+req.Model),
		model.WithEventAttributes(attrs),
	)
	h.emitter.Emit(ctx, event)

	// A stream cut short is not an OnError case: the client was answered with a
	// 200 and received part of the answer, so it never reaches the error hook.
	// It gets an event of its own, on top of the one above, because that is the
	// only place the interruption rate can be read from.
	if res.Interruption != nil {
		interruptAttrs := make(map[string]string, len(attrs)+3)
		for k, v := range attrs {
			interruptAttrs[k] = v
		}
		interruptAttrs["cause"] = string(res.Interruption.Cause)
		interruptAttrs["chunks_emitted"] = strconv.Itoa(res.Interruption.ChunksEmitted)
		if res.Interruption.Err != nil {
			interruptAttrs["error"] = truncate(res.Interruption.Err.Error(), maxErrorAttributeLength)
		}

		interrupted := model.NewEvent(model.EventSourcePlatform, model.EventTypeProxyStreamInterrupted,
			model.WithEventOrg(orgID),
			model.WithEventUser(model.UserID(req.UserID)),
			model.WithEventSeverity(interruptionSeverity(res.Interruption.Cause)),
			model.WithEventMessage("Flux proxy interrompu ("+string(res.Interruption.Cause)+"): "+req.Model),
			model.WithEventAttributes(interruptAttrs),
		)
		h.emitter.Emit(ctx, interrupted)
	}

	return nil, nil
}

// interruptionSeverity rates a cause by who is at fault. A provider failing and
// a write failing are faults worth an alert; a client closing its tab is
// ordinary traffic, and a provider ending a stream without a terminal chunk is
// common enough that warning on it would drown the signal.
func interruptionSeverity(cause genaiProxy.StreamInterruptionCause) model.EventSeverity {
	switch cause {
	case genaiProxy.StreamInterruptionUpstream, genaiProxy.StreamInterruptionWriteFailed:
		return model.SeverityWarning
	default:
		return model.SeverityInfo
	}
}

// OnError implements proxy.ErrorHook. A client that hangs up before the
// answer is not a failure of the platform and is left out.
func (h *XoloEventEmitterHook) OnError(ctx context.Context, req *genaiProxy.ProxyRequest, err error) (*genaiProxy.HookResult, error) {
	if req.UserID == "" || err == nil || errors.Is(err, context.Canceled) {
		return nil, nil
	}

	orgID, attrs := h.baseAttributes(ctx, req)

	status := http.StatusInternalServerError
	var httpErr *llm.HTTPError
	if errors.As(err, &httpErr) {
		status = httpErr.StatusCode
	}
	attrs["status"] = strconv.Itoa(status)
	attrs["error"] = truncate(err.Error(), maxErrorAttributeLength)

	event := model.NewEvent(model.EventSourcePlatform, model.EventTypeProxyRequestFailed,
		model.WithEventOrg(orgID),
		model.WithEventUser(model.UserID(req.UserID)),
		model.WithEventSeverity(model.SeverityWarning),
		model.WithEventMessage("Requête proxy en échec ("+strconv.Itoa(status)+"): "+req.Model),
		model.WithEventAttributes(attrs),
	)
	h.emitter.Emit(ctx, event)

	return nil, nil
}

// truncate keeps the first max runes of s, marking the cut with an ellipsis.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

var (
	_ genaiProxy.PostResponseHook = &XoloEventEmitterHook{}
	_ genaiProxy.ErrorHook        = &XoloEventEmitterHook{}
)
