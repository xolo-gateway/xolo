package authn

import (
	"encoding/json"
	"slices"
	"strconv"
)

const (
	// The claims of a bearer token are never stored: they are only bounded
	// against a provider sending anything.
	maxClaimsSize  = 16 * 1024
	maxClaimValues = 256
	// A session lives in a cookie, whose encoded value can not exceed 4096
	// bytes and also carries the rest of the identity.
	maxSessionClaimsSize  = 1024
	maxSessionClaimValues = 32
)

// protocolClaims describe the token rather than the person.
var protocolClaims = map[string]struct{}{
	"acr": {}, "amr": {}, "at_hash": {}, "auth_time": {}, "azp": {}, "c_hash": {},
	"exp": {}, "iat": {}, "jti": {}, "nbf": {}, "nonce": {}, "sid": {},
}

// Claims are the values the identity provider asserted about an identity,
// each one as a list: a claim holding a single value is a list of one.
type Claims map[string][]string

// ClaimsSource decodes claims on demand. The claims compared lists, per claim
// name, the values the rules compare: those claims come first, reduced to
// those values when they do not fit whole. It also returns the names of the
// claims it dropped.
type ClaimsSource func(compared map[string][]string) (Claims, []string)

// PayloadClaims decodes the claims of a JSON payload within what NewClaims
// keeps.
func PayloadClaims(payload []byte) ClaimsSource {
	return func(compared map[string][]string) (Claims, []string) {
		var raw map[string]any
		if err := json.Unmarshal(payload, &raw); err != nil {
			return nil, nil
		}
		return newClaims(raw, maxClaimsSize, maxClaimValues, compared)
	}
}

// ClaimSet returns the claims of the identity, decoding them when it has a
// ClaimsSource, and the names of the claims that did not fit.
func (u *User) ClaimSet(compared map[string][]string) (Claims, []string) {
	if u.Claims == nil && u.ClaimsSource != nil {
		return u.ClaimsSource(compared)
	}
	return u.Claims, nil
}

// Has reports whether the claim holds this exact value.
func (c Claims) Has(name, value string) bool {
	return slices.Contains(c[name], value)
}

// NewClaims keeps the strings, booleans and lists of strings of raw, which
// are the claims a rule can compare, up to maxClaimsSize. It also returns the
// names of the claims it dropped: PayloadClaims then makes room for those the
// rules compare.
func NewClaims(raw map[string]any) (Claims, []string) {
	return newClaims(raw, maxClaimsSize, maxClaimValues, nil)
}

// NewSessionClaims is NewClaims within what a session cookie can carry. The
// claims compared lists, per claim name, the values the rules compare: those
// claims come first, reduced to those values when they do not fit whole. It
// also returns the names of the claims it dropped, for they then match no
// rule.
func NewSessionClaims(raw map[string]any, compared map[string][]string) (Claims, []string) {
	return newClaims(raw, maxSessionClaimsSize, maxSessionClaimValues, compared)
}

// newClaims takes the compared claims, then the others, each in name order
// until maxSize, so the same token always gives the same result, and drops
// whole a claim that does not fit.
func newClaims(raw map[string]any, maxSize, maxValues int, compared map[string][]string) (Claims, []string) {
	var first, then []string
	for name := range raw {
		switch _, skip := protocolClaims[name]; {
		case skip:
		case len(compared[name]) > 0:
			first = append(first, name)
		default:
			then = append(then, name)
		}
	}
	slices.Sort(first)
	slices.Sort(then)

	claims := Claims{}
	var dropped []string
	size := 0
	fits := func(name string, values []string) bool {
		claimSize := len(name)
		for _, value := range values {
			claimSize += len(value)
		}
		if len(values) == 0 || len(values) > maxValues || size+claimSize > maxSize {
			return false
		}
		size += claimSize
		claims[name] = values
		return true
	}
	for _, name := range first {
		values := claimValues(raw[name])
		if len(values) == 0 || fits(name, values) {
			continue
		}
		matching := slices.DeleteFunc(slices.Clone(values), func(value string) bool {
			return !slices.Contains(compared[name], value)
		})
		if len(matching) > 0 && !fits(name, matching) {
			dropped = append(dropped, name)
		}
	}
	for _, name := range then {
		values := claimValues(raw[name])
		if len(values) > 0 && !fits(name, values) {
			dropped = append(dropped, name)
		}
	}
	return claims, dropped
}

func claimValues(raw any) []string {
	switch v := raw.(type) {
	case string:
		return []string{v}
	case bool:
		return []string{strconv.FormatBool(v)}
	case []string:
		return slices.Clone(v)
	case []any:
		values := make([]string, 0, len(v))
		for _, item := range v {
			value, ok := item.(string)
			if !ok {
				return nil
			}
			values = append(values, value)
		}
		return values
	}
	return nil
}
