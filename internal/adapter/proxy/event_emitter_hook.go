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

	event := model.NewEvent(model.EventSourcePlatform, model.EventTypeProxyRequest,
		model.WithEventOrg(orgID),
		model.WithEventUser(model.UserID(req.UserID)),
		model.WithEventSeverity(model.SeverityInfo),
		model.WithEventMessage("Requête proxy: "+req.Model),
		model.WithEventAttributes(attrs),
	)
	h.emitter.Emit(ctx, event)

	return nil, nil
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
