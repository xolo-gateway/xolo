// Package events provides store decorators that emit platform events on
// create/update/delete operations. Wrapping the stores (rather than sprinkling
// emit calls across HTTP handlers) guarantees every caller — web UI, API,
// future callers — produces the same lifecycle events, and keeps the emission
// concern out of the transport layer.
package events

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// emit attributes local events to an authenticated user or an explicit provisioning actor.
func emit(ctx context.Context, emitter port.EventEmitter, orgID model.OrgID, severity model.EventSeverity, typ, message string, attrs map[string]string) {
	if emitter == nil {
		return
	}
	user := httpCtx.User(ctx)
	actor := model.ActorFromContext(ctx)
	if user == nil && actor.RequestID == "" {
		return
	}
	if attrs == nil {
		attrs = map[string]string{}
	}
	// actor_uri and request_id record the transport origin; actor and actor_id
	// name who acted, an authenticated user taking precedence over the URI.
	if actor.RequestID != "" {
		attrs["actor_uri"] = actor.URI
		attrs["request_id"] = actor.RequestID
	}
	if user != nil {
		attrs["actor"] = user.DisplayName()
		attrs["actor_id"] = string(user.ID())
	} else {
		attrs["actor"] = actor.URI
		attrs["actor_id"] = string(actor.UserID)
	}

	emitter.Emit(ctx, model.NewEvent(model.EventSourcePlatform, typ,
		model.WithEventOrg(orgID),
		model.WithEventSeverity(severity),
		model.WithEventMessage(message),
		model.WithEventAttributes(attrs),
	))
}
