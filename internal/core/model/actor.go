package model

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// Actor describes the origin independently of HTTP, sessions or mTLS.
type Actor struct {
	UserID    UserID `json:"user_id,omitempty"`
	URI       string `json:"uri,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}
type actorKey struct{}

func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}
func ActorFromContext(ctx context.Context) Actor {
	if actor, ok := ctx.Value(actorKey{}).(Actor); ok {
		return actor
	}
	return Actor{URI: "urn:xolo:operator:local"}
}

// NewRequestID produces an opaque correlation ID, with no ordering guarantee.
func NewRequestID() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

// EnsureActor fixes the actor and correlation before the first transaction attempt.
func EnsureActor(ctx context.Context) context.Context {
	actor := ActorFromContext(ctx)
	if actor.RequestID == "" {
		actor.RequestID = NewRequestID()
	}
	return WithActor(ctx, actor)
}
