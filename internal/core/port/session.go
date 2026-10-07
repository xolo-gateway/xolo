package port

import (
	"context"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// SessionRegistry persists the interactive OIDC sessions, so a logout holds
// across restarts and replicas. It never stores a cookie, a raw token nor a
// provider secret, only digests of the identities. CheckSession is a plain
// read: it takes no lock, and no operation takes the publication lock of the
// event feed. Opening a session and revoking an identity serialize on that
// identity alone.
type SessionRegistry interface {
	// OpenSession registers a session and returns its identifier. It returns
	// ErrNotAllowed when the sign-in started before a revocation of the
	// identity, or when the session is malformed or already expired.
	OpenSession(ctx context.Context, session model.OIDCSession) (string, error)
	// CheckSession returns ErrNotFound unless the session is registered for
	// that tenant and identity and has not expired.
	CheckSession(ctx context.Context, id string, tenant model.TenantID, issuer, subject string) error
	// CloseSession removes a session. Closing an unknown session succeeds.
	CloseSession(ctx context.Context, id string) error
	// RevokeIdentitySessions removes every session of the identity, in every
	// tenant, and refuses the sign-ins started before now. jti identifies the
	// logout token: a token already processed changes nothing and returns
	// false.
	RevokeIdentitySessions(ctx context.Context, issuer, subject, jti string, issuedAt, expiresAt time.Time) (bool, error)
	// SweepSessions removes the expired sessions, the replay keys of tokens
	// that can no longer be accepted, and the identities unused for longer
	// than model.OIDCIdentityRetention(sessionTTL).
	SweepSessions(ctx context.Context, now time.Time, sessionTTL time.Duration) error
}
