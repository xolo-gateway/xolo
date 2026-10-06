//go:build e2e

package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gormadapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

const provisioningURI = "urn:xolo:e2e:provisioner"

var provisioningEndpoint struct {
	url    string
	client *http.Client
}

// configureProvisioning enables the real process's dedicated listener with a
// throwaway CA and client. All private key material stays in the test directory.
func configureProvisioning(dir string) ([]string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "e2e-ca"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caPath := filepath.Join(dir, "provisioning-ca.pem")
	if err := os.WriteFile(caPath, caPEM, 0600); err != nil {
		return nil, err
	}
	issue := func(server bool) ([]byte, []byte, error) {
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature}
		if server {
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			leaf.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		} else {
			leaf.SerialNumber = big.NewInt(3)
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
			u, _ := url.Parse(provisioningURI)
			leaf.URIs = []*url.URL{u}
		}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
		if err != nil {
			return nil, nil, err
		}
		private, err := x509.MarshalPKCS8PrivateKey(leafKey)
		if err != nil {
			return nil, nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), nil
	}
	serverCert, serverKey, err := issue(true)
	if err != nil {
		return nil, err
	}
	certPath, keyPath := filepath.Join(dir, "provisioning-server.pem"), filepath.Join(dir, "provisioning-server-key.pem")
	if err := os.WriteFile(certPath, serverCert, 0600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, serverKey, 0600); err != nil {
		return nil, err
	}
	clientCert, clientKey, err := issue(false)
	if err != nil {
		return nil, err
	}
	clientPair, err := tls.X509KeyPair(clientCert, clientKey)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	provisioningEndpoint.client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{clientPair}, MinVersion: tls.VersionTLS13}}}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	address := fmt.Sprintf("127.0.0.1:%d", port)
	provisioningEndpoint.url = "https://" + address
	return []string{"XOLO_PROVISIONNING_API_ENABLED=true", "XOLO_PROVISIONNING_API_ADDRESS=" + address, "XOLO_PROVISIONNING_API_TLS_CERT_FILE=" + certPath, "XOLO_PROVISIONNING_API_TLS_KEY_FILE=" + keyPath, "XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE=" + caPath, "XOLO_PROVISIONNING_API_AUTHORIZED_URIS=" + provisioningURI}, nil
}

func TestProvisioningComposedMutationAudit(t *testing.T) {
	db, err := openDB(env.dsn)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	var tenant gormadapter.Tenant
	require.NoError(t, db.First(&tenant, "slug = ?", model.DefaultTenantSlug).Error)
	requestID := "0123456789abcdef0123456789abcdef"
	payload := `{"slug":"e2e-provisioning","name":"Provisioned through mTLS","owner":{"provider":"oidc","subject":"e2e-provisioned-owner","displayName":"E2E Owner"}}`
	req, err := http.NewRequestWithContext(t.Context(), "POST", provisioningEndpoint.url+"/v1/tenants/"+tenant.ID+"/organizations", strings.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", requestID)
	resp, err := provisioningEndpoint.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))
	require.Equal(t, requestID, resp.Header.Get("X-Request-ID"))
	var audits []gormadapter.MutationAudit
	require.NoError(t, db.Where("request_id = ?", requestID).Find(&audits).Error)
	require.Len(t, audits, 6)
	for _, audit := range audits {
		require.Equal(t, tenant.ID, audit.TenantID)
		require.Equal(t, "null", audit.Before)
		var actor model.Actor
		require.NoError(t, json.Unmarshal([]byte(audit.Actor), &actor))
		require.Equal(t, provisioningURI, actor.URI)
		require.Empty(t, actor.UserID)
	}
}
