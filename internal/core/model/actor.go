package model

import "context"

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
