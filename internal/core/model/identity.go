package model

import (
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxIdentityIssuerLength  = 2048
	maxIdentitySubjectLength = 255
)

// Identity is an external identity declared by provisioning for a member: the
// exact issuer and subject an identity provider asserts at sign-in. It is
// unique per tenant and never normalized: subject case and spaces, and a
// trailing slash on the issuer, are significant.
type Identity struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

// Valid reports whether the identity is a public HTTPS issuer (with a host,
// without userinfo, query, fragment, surrounding whitespace or control
// characters) and a nonempty subject of at most 255 bytes without control
// characters. It preserves the exact bytes and performs no network access.
func (i Identity) Valid() bool {
	if !validIdentityText(i.Issuer, maxIdentityIssuerLength) || !validIdentityText(i.Subject, maxIdentitySubjectLength) {
		return false
	}
	if strings.TrimSpace(i.Issuer) != i.Issuer || strings.ContainsAny(i.Issuer, "?#") {
		return false
	}
	u, err := url.Parse(i.Issuer)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

func validIdentityText(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// IdentityIssuers maps a local authentication provider ID to the issuer its
// sign-ins prove. A provider without a verifiable issuer (GitHub OAuth, a
// static Gitea) is absent: its sign-ins can never match a declared identity.
type IdentityIssuers map[string]string

// Issuer returns the issuer proven by the provider, if any.
func (m IdentityIssuers) Issuer(provider string) (string, bool) {
	issuer, ok := m[provider]
	return issuer, ok && issuer != ""
}

// Providers returns, sorted, every provider ID proving the issuer.
func (m IdentityIssuers) Providers(issuer string) []string {
	var providers []string
	for provider, i := range m {
		if i == issuer && issuer != "" {
			providers = append(providers, provider)
		}
	}
	sort.Strings(providers)
	return providers
}
