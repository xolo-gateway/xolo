package provisionning

import (
	"crypto/tls"
	"crypto/x509"
	"os"

	"github.com/pkg/errors"
)

// LoadTLSConfig builds the mutual TLS configuration of the Provisionning API listener.
//
// It is called at startup, before the listener is opened, so a misconfigured
// certificate, key or CA bundle fails the process immediately rather than on
// the first request.
func LoadTLSConfig(certFile, keyFile, clientCAFile string, authorizedURIs ...string) (*tls.Config, error) {
	if certFile == "" {
		return nil, errors.New("no server certificate file provided")
	}
	if keyFile == "" {
		return nil, errors.New("no server private key file provided")
	}
	if clientCAFile == "" {
		return nil, errors.New("no client certificate authority file provided")
	}

	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, errors.Wrap(err, "could not load server certificate and private key")
	}

	rawCA, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, errors.Wrap(err, "could not read client certificate authority file")
	}

	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(rawCA) {
		return nil, errors.New("client certificate authority file contains no valid PEM certificate")
	}

	if len(authorizedURIs) == 0 {
		return nil, errors.New("authorized client URIs are required")
	}
	allowed := map[string]bool{}
	for _, uri := range authorizedURIs {
		allowed[uri] = true
	}
	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		// The TLS stack itself rejects any connection without a client
		// certificate signed by one of the configured authorities, before any
		// handler runs. There is no anonymous nor user-authenticated fallback.
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  clientCAs,
		MinVersion: tls.VersionTLS13,
		VerifyConnection: func(state tls.ConnectionState) error {
			if _, ok := authorizedURI(&state, allowed); !ok {
				return errors.New("client certificate rejected")
			}
			return nil
		},
	}, nil
}
