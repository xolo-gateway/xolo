package authn

import (
	"encoding/json"
	"strings"
)

type User struct {
	Email       string
	Provider    string
	Subject     string
	DisplayName string
	OrgID       string
	TokenID     string

	// Issuer is the issuer the identity provider proved for this sign-in, or
	// empty when the provider proves none (GitHub OAuth, a static Gitea).
	// Only a proven issuer can match an identity declared by provisioning.
	Issuer string

	// EmailVerified tells that the identity provider asserted the email as
	// verified. Only such an email can attach a sign-in to an existing account.
	EmailVerified bool

	// AccountID designates the Xolo account directly. It is set only by the
	// API-token authenticator, which already resolved the token's owner: the
	// bridge then never resolves nor provisions anything.
	AccountID string

	// TenantID is the tenant the identity was authenticated on. It is stamped
	// on the session so a cookie set on a parent domain can not carry an
	// identity from one tenant to another: an authenticator that finds a
	// mismatch treats the session as absent.
	TenantID string

	// SessionID designates the registered session an interactive OIDC
	// sign-in opened. A session cookie without one, or whose session is no
	// longer registered, authenticates nothing.
	SessionID string
}

// IsTrue reads a boolean claim some identity providers encode as a string
// ("true"), like email_verified.
func IsTrue(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	}
	return false
}

// BoolClaim decodes a boolean claim encoded either as a JSON boolean or as a
// string. Any other value reads as false.
type BoolClaim bool

// UnmarshalJSON implements json.Unmarshaler.
func (b *BoolClaim) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*b = BoolClaim(IsTrue(value))
	return nil
}
