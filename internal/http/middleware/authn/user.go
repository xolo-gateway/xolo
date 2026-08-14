package authn

type User struct {
	Email       string
	Provider    string
	Subject     string
	DisplayName string
	OrgID       string
	TokenID     string

	// TenantID is the tenant the identity was authenticated on. It is stamped
	// on the session so a cookie set on a parent domain can not carry an
	// identity from one tenant to another: an authenticator that finds a
	// mismatch treats the session as absent.
	TenantID string
}
