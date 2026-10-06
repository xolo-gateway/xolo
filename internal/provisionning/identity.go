package provisionning

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/url"
	"strings"
)

// ClientIdentity describes the client certificate that authenticated a
// Provisionning API request.
//
// Only the verified, authorized URI grants authority. Other fields are metadata.
type ClientIdentity struct {
	URI          string
	CommonName   string
	SerialNumber string
	Subject      string
}

func newClientIdentity(cert *x509.Certificate) *ClientIdentity {
	return &ClientIdentity{
		CommonName:   cert.Subject.CommonName,
		SerialNumber: cert.SerialNumber.String(),
		Subject:      cert.Subject.String(),
	}
}

type contextKey struct{}

var clientIdentityContextKey contextKey

func setClientIdentity(ctx context.Context, identity *ClientIdentity) context.Context {
	return context.WithValue(ctx, clientIdentityContextKey, identity)
}

// CurrentClientIdentity returns the identity of the client certificate that
// authenticated the request, if any.
func CurrentClientIdentity(ctx context.Context) (*ClientIdentity, bool) {
	identity, ok := ctx.Value(clientIdentityContextKey).(*ClientIdentity)
	return identity, ok
}

func clientAllowlist(uris []string) (map[string]bool, error) {
	if len(uris) == 0 {
		return nil, fmt.Errorf("authorized client URIs are required")
	}
	allowed := make(map[string]bool, len(uris))
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || (u.Host == "" && u.Opaque == "" && u.Path == "") || strings.ContainsAny(raw, " \t\r\n") || allowed[raw] {
			return nil, fmt.Errorf("authorized client URIs must be distinct absolute URIs")
		}
		allowed[raw] = true
	}
	return allowed, nil
}
