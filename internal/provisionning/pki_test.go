package provisionning_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testPKI is a throwaway certificate authority used to exercise the mutual TLS
// configuration end to end.
type testPKI struct {
	dir string

	caCertPEM  []byte
	caCert     *x509.Certificate
	caKey      *ecdsa.PrivateKey
	caCertFile string
}

func newTestPKI(t *testing.T, name string) *testPKI {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ca key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          newSerialNumber(t),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create ca certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse ca certificate: %v", err)
	}

	pki := &testPKI{
		dir:       t.TempDir(),
		caCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		caCert:    cert,
		caKey:     key,
	}

	pki.caCertFile = pki.writeFile(t, "ca.crt", pki.caCertPEM)

	return pki
}

// issue signs a leaf certificate and returns the paths of its PEM files.
func (p *testPKI) issue(t *testing.T, name string, serverAuth bool, customize ...func(*x509.Certificate)) (certFile, keyFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: newSerialNumber(t),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}

	if serverAuth {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		template.DNSNames = []string{"localhost"}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	} else {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		uri, _ := url.Parse("urn:xolo:test:console")
		template.URIs = []*url.URL{uri}
	}

	for _, f := range customize {
		f(template)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	rawKey, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	certFile = p.writeFile(t, name+".crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyFile = p.writeFile(t, name+".key", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: rawKey}))

	return certFile, keyFile
}

func (p *testPKI) writeFile(t *testing.T, name string, content []byte) string {
	t.Helper()

	path := filepath.Join(p.dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}

	return path
}

func newSerialNumber(t *testing.T) *big.Int {
	t.Helper()

	limit := new(big.Int).Lsh(big.NewInt(1), 128)

	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		t.Fatalf("generate serial number: %v", err)
	}

	return serial
}
