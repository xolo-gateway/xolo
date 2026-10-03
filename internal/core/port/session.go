package port

import (
	"context"
	"time"
)

// SessionRegistry never stores cookies, raw OIDC tokens, or provider secrets.
// Revocation and replay protection commit in the same serialized transaction.
type SessionRegistry interface {
	OpenSession(ctx context.Context, issuer, subject string, authenticatedAt, expiresAt time.Time) (string, error)
	CheckSession(ctx context.Context, id, issuer, subject string) error
	CloseSession(ctx context.Context, id string) error
	RevokeIdentitySessions(ctx context.Context, issuer, subject, jti string, issuedAt time.Time) error
}
