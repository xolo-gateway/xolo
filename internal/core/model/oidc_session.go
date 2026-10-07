package model

import "time"

// OIDCSession describes an interactive OIDC sign-in to register. Its cookie
// only carries the identifier the registry returns: the registry decides,
// on every request, whether the session still stands.
type OIDCSession struct {
	TenantID TenantID
	// Issuer is the issuer the provider proved, or the provider pseudo-issuer
	// (see OIDCPseudoIssuer) when it proves none.
	Issuer  string
	Subject string
	// AuthenticatedAt is when the sign-in started at Xolo. A sign-in started
	// before a revocation of its identity is refused.
	AuthenticatedAt time.Time
	ExpiresAt       time.Time
}

const (
	// LogoutTokenMaxAge bounds both the age of an accepted logout token and
	// its lifetime (exp - iat).
	LogoutTokenMaxAge = 5 * time.Minute
	// LogoutClockSkew is the clock difference tolerated between the identity
	// provider and the replicas.
	LogoutClockSkew = 5 * time.Minute
	// LoginMaxDuration bounds the time between the start of a sign-in and its
	// callback.
	LoginMaxDuration = 15 * time.Minute
)

// LogoutReplayExpiry returns until when the replay key of a logout token must
// be kept. Past it, the token is already refused for its age, whatever the
// replica checking it: removing the key never reopens a replay window.
func LogoutReplayExpiry(issuedAt, expiresAt time.Time) time.Time {
	until := issuedAt.Add(LogoutTokenMaxAge)
	if expiresAt.After(until) {
		until = expiresAt
	}
	return until.Add(LogoutClockSkew)
}

// OIDCIdentityRetention returns how long the revocation watermark of an
// identity must outlive its last use: any session opened before it has
// expired by then, and any sign-in started before it has timed out.
func OIDCIdentityRetention(sessionTTL time.Duration) time.Duration {
	return sessionTTL + LoginMaxDuration + LogoutClockSkew
}

// OIDCPseudoIssuer designates the sign-ins of a provider that proves no
// issuer (GitHub OAuth, a static Gitea). No logout token can name it.
func OIDCPseudoIssuer(providerID string) string {
	return "urn:xolo:provider:" + providerID
}
