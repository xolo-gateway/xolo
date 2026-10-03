package authn

import "time"

type User struct {
	AccountID       string // Set only by the internal API-token authenticator.
	Issuer          string
	EmailVerified   bool
	SessionID       string
	AuthenticatedAt time.Time
	Email           string
	Provider        string
	Subject         string
	DisplayName     string
	OrgID           string
	TokenID         string

	// TenantID is the tenant the identity was authenticated on. It is stamped
	// on the session so a cookie set on a parent domain can not carry an
	// identity from one tenant to another: an authenticator that finds a
	// mismatch treats the session as absent.
	TenantID string
}
