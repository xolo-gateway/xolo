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

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormadapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

const provisioningURI = "urn:xolo:e2e:provisioner"

// provisioningClient reaches the provisioning listener of one server process.
type provisioningClient struct {
	url    string
	client *http.Client
}

// provisioningEndpoint is the provisioning listener of the shared server.
var provisioningEndpoint provisioningClient

// configureProvisioning enables the real process's dedicated listener with a
// throwaway CA and client. All private key material stays in dir.
func configureProvisioning(dir string) ([]string, provisioningClient, error) {
	var endpoint provisioningClient

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, endpoint, err
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "e2e-ca"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return nil, endpoint, err
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		return nil, endpoint, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caPath := filepath.Join(dir, "provisioning-ca.pem")
	if err := os.WriteFile(caPath, caPEM, 0600); err != nil {
		return nil, endpoint, err
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
		return nil, endpoint, err
	}
	certPath, keyPath := filepath.Join(dir, "provisioning-server.pem"), filepath.Join(dir, "provisioning-server-key.pem")
	if err := os.WriteFile(certPath, serverCert, 0600); err != nil {
		return nil, endpoint, err
	}
	if err := os.WriteFile(keyPath, serverKey, 0600); err != nil {
		return nil, endpoint, err
	}
	clientCert, clientKey, err := issue(false)
	if err != nil {
		return nil, endpoint, err
	}
	clientPair, err := tls.X509KeyPair(clientCert, clientKey)
	if err != nil {
		return nil, endpoint, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	endpoint.client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{clientPair}, MinVersion: tls.VersionTLS13}}}
	port, err := freePort()
	if err != nil {
		return nil, endpoint, err
	}
	address := fmt.Sprintf("127.0.0.1:%d", port)
	endpoint.url = "https://" + address
	return []string{"XOLO_PROVISIONNING_API_ENABLED=true", "XOLO_PROVISIONNING_API_ADDRESS=" + address, "XOLO_PROVISIONNING_API_TLS_CERT_FILE=" + certPath, "XOLO_PROVISIONNING_API_TLS_KEY_FILE=" + keyPath, "XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE=" + caPath, "XOLO_PROVISIONNING_API_AUTHORIZED_URIS=" + provisioningURI}, endpoint, nil
}

// provision sends one request to a provisioning listener and returns the
// status and body, checking the X-Request-ID echo.
func (p provisioningClient) provision(t *testing.T, method, path, requestID string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(mustJSON(body)))
	}
	req, err := http.NewRequestWithContext(t.Context(), method, p.url+path, reader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
	resp, err := p.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if requestID != "" {
		require.Equal(t, requestID, resp.Header.Get("X-Request-ID"))
	}
	return resp.StatusCode, raw
}

// newRequestID returns a client-supplied request ID the listener accepts.
func newRequestID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

// TestProvisioningComposedMutationAudit provisions an organization and its
// owner through the common PUTs and checks that each request records its own
// audit rows, attributed to the mTLS client, and that a replay records none.
func TestProvisioningComposedMutationAudit(t *testing.T) {
	db, err := openDB(env.dsn)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	var tenant gormadapter.Tenant
	require.NoError(t, db.First(&tenant, "slug = ?", model.DefaultTenantSlug).Error)

	tenantPath := "/v1/tenants/" + tenant.ID
	orgID := uuid.NewString()
	orgBody := map[string]any{"slug": "e2e-provisioning", "name": "Provisioned through mTLS", "status": "active"}
	memberBody := map[string]any{"email": "e2e-owner@example.tld", "display_name": "E2E Owner", "tenant_role": "member", "status": "active"}
	membershipBody := map[string]any{"role": "owner", "status": "active"}

	audits := func(requestID string) []gormadapter.MutationAudit {
		t.Helper()
		var rows []gormadapter.MutationAudit
		require.NoError(t, db.Where("request_id = ?", requestID).Order("resource, resource_id").Find(&rows).Error)
		for _, audit := range rows {
			require.Equal(t, tenant.ID, audit.TenantID)
			var actor model.Actor
			require.NoError(t, json.Unmarshal([]byte(audit.Actor), &actor))
			require.Equal(t, provisioningURI, actor.URI)
			require.Equal(t, requestID, actor.RequestID)
			require.Empty(t, actor.UserID)
		}
		return rows
	}
	resources := func(rows []gormadapter.MutationAudit) map[string]int {
		counts := map[string]int{}
		for _, audit := range rows {
			counts[audit.Resource]++
		}
		return counts
	}

	// The organization is created with its three builtin roles.
	orgRequest := newRequestID()
	status, body := provisioningEndpoint.provision(t, http.MethodPut, tenantPath+"/organizations/"+orgID, orgRequest, orgBody)
	require.Equal(t, http.StatusOK, status, string(body))
	rows := audits(orgRequest)
	require.Equal(t, map[string]int{"organization": 1, "role": 3}, resources(rows))
	for _, audit := range rows {
		require.Equal(t, orgID, audit.OrgID)
		require.Equal(t, "null", audit.Before)
	}

	// The common member PUT only updates users: the owner signs in first,
	// here through the identity upsert of the extensions.
	userRequest := newRequestID()
	status, body = provisioningEndpoint.provision(t, http.MethodPut, "/v1/xolo/tenants/"+tenant.ID+"/users", userRequest,
		map[string]any{"provider": "oidc", "subject": "e2e-provisioned-owner"})
	require.Equal(t, http.StatusCreated, status, string(body))
	var user struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &user))
	rows = audits(userRequest)
	require.Len(t, rows, 1)
	require.Equal(t, "user", rows[0].Resource)
	require.Equal(t, user.ID, rows[0].ResourceID)
	require.Equal(t, "null", rows[0].Before)

	memberRequest := newRequestID()
	status, body = provisioningEndpoint.provision(t, http.MethodPut, tenantPath+"/members/"+user.ID, memberRequest, memberBody)
	require.Equal(t, http.StatusOK, status, string(body))
	rows = audits(memberRequest)
	require.Len(t, rows, 1)
	require.Equal(t, "user", rows[0].Resource)
	require.NotEqual(t, "null", rows[0].Before)
	require.Contains(t, rows[0].After, `"email":"e2e-owner@example.tld"`)
	// The identity of the signed-in user survives the PUT.
	require.Contains(t, rows[0].After, `"subject":"e2e-provisioned-owner"`)

	membershipRequest := newRequestID()
	status, body = provisioningEndpoint.provision(t, http.MethodPut, tenantPath+"/organizations/"+orgID+"/members/"+user.ID, membershipRequest, membershipBody)
	require.Equal(t, http.StatusOK, status, string(body))
	rows = audits(membershipRequest)
	require.Len(t, rows, 1)
	require.Equal(t, "membership", rows[0].Resource)
	require.Equal(t, orgID, rows[0].OrgID)
	require.Equal(t, "null", rows[0].Before)
	require.Contains(t, rows[0].After, `"common_role":"owner"`)

	// Replaying the declarations changes nothing and records nothing.
	replayRequest := newRequestID()
	for path, payload := range map[string]map[string]any{
		tenantPath + "/organizations/" + orgID:                         orgBody,
		tenantPath + "/members/" + user.ID:                             memberBody,
		tenantPath + "/organizations/" + orgID + "/members/" + user.ID: membershipBody,
	} {
		status, body = provisioningEndpoint.provision(t, http.MethodPut, path, replayRequest, payload)
		require.Equal(t, http.StatusOK, status, string(body))
	}
	require.Empty(t, audits(replayRequest))
}

// TestProvisioningDomainRouting starts a multi-tenant server on a snapshot of
// the seeded database, declares domains through its provisioning listener and
// checks that the public server routes requests by Host header.
func TestProvisioningDomainRouting(t *testing.T) {
	dir := t.TempDir()

	// VACUUM INTO takes a consistent copy while the shared server runs.
	dsn := filepath.Join(dir, "multitenant.sqlite")
	db, err := openDB(env.dsn)
	require.NoError(t, err)
	require.NoError(t, db.Exec("VACUUM INTO ?", dsn).Error)
	var tenant gormadapter.Tenant
	require.NoError(t, db.First(&tenant, "slug = ?", model.DefaultTenantSlug).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	port, err := freePort()
	require.NoError(t, err)
	publicURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	provisioningEnv, provisioning, err := configureProvisioning(dir)
	require.NoError(t, err)
	config := append(serverEnv(env.pluginsDir, port, fmt.Sprintf("http://xolo.e2e.test:%d", port), dsn), provisioningEnv...)
	config = append(config, "XOLO_MULTITENANCY_ENABLED=true")
	logPath := filepath.Join(dir, "server.log")
	stop, err := launchServer(env.serverBin, logPath, config)
	require.NoError(t, err)
	t.Cleanup(stop)
	t.Cleanup(func() {
		if t.Failed() {
			if logs, err := os.ReadFile(logPath); err == nil {
				t.Logf("---- multi-tenant server log ----\n%s", logs)
			}
		}
	})

	// The provisioning listener answers once the stores are ready; the public
	// listener starts alongside it.
	require.Eventually(t, func() bool {
		resp, err := provisioning.client.Get(provisioning.url + "/v1/manifest")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 90*time.Second, 250*time.Millisecond, "provisioning listener not ready")

	get := func(host string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, publicURL+"/api/v1/models", nil)
		require.NoError(t, err)
		req.Host = host
		req.Header.Set("Authorization", "Bearer "+tokenAlice)
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	require.Eventually(t, func() bool { return get("127.0.0.1") != 0 }, 30*time.Second, 250*time.Millisecond, "public listener not ready")

	// No domain is declared yet: every host is unknown.
	require.Equal(t, http.StatusNotFound, get("127.0.0.1"))
	require.Equal(t, http.StatusNotFound, get("default.e2e.test"))

	put := func(path string, body any) {
		t.Helper()
		status, raw := provisioning.provision(t, http.MethodPut, path, newRequestID(), body)
		require.Equal(t, http.StatusOK, status, string(raw))
	}
	active := map[string]any{"status": "active"}

	// The seeded default tenant gets a domain: Alice's token works there.
	put("/v1/tenants/"+tenant.ID+"/domains/default.e2e.test", active)
	require.Equal(t, http.StatusOK, get("default.e2e.test"))
	require.Equal(t, http.StatusOK, get(fmt.Sprintf("default.e2e.test:%d", port)))

	// A new tenant and its domain: the host is routed to it, where Alice's
	// token, issued in the default tenant, means nothing.
	otherID := uuid.NewString()
	put("/v1/tenants/"+otherID, map[string]any{"slug": "e2e-domain", "name": "E2E domain", "status": "active"})
	put("/v1/tenants/"+otherID+"/domains/other.e2e.test", active)
	require.Equal(t, http.StatusUnauthorized, get("other.e2e.test"))

	// Undeclared hosts and IP literals stay unknown.
	require.Equal(t, http.StatusNotFound, get("undeclared.e2e.test"))
	require.Equal(t, http.StatusNotFound, get("127.0.0.1"))

	// A suspended domain no longer routes.
	put("/v1/tenants/"+tenant.ID+"/domains/default.e2e.test", map[string]any{"status": "suspended"})
	require.Equal(t, http.StatusNotFound, get("default.e2e.test"))
}

// exchange sends one request with headers to the provisioning listener.
func (p provisioningClient) exchange(t *testing.T, method, path string, headers map[string]string, body any) (int, http.Header, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(mustJSON(body)))
	}
	req, err := http.NewRequestWithContext(t.Context(), method, p.url+path, reader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := p.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, raw
}

// TestProvisioningConditionalSync reads, conditionally writes and follows the
// event feed of the real server, including a change made outside the API.
func TestProvisioningConditionalSync(t *testing.T) {
	db, err := openDB(env.dsn)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	var tenant gormadapter.Tenant
	require.NoError(t, db.First(&tenant, "slug = ?", model.DefaultTenantSlug).Error)
	orgPath := "/v1/tenants/" + tenant.ID + "/organizations/" + uuid.NewString()

	status, _, body := provisioningEndpoint.exchange(t, http.MethodGet, "/v1/events/cursor", nil, nil)
	require.Equal(t, http.StatusOK, status, string(body))
	var start struct {
		Cursor string `json:"cursor"`
	}
	require.NoError(t, json.Unmarshal(body, &start))

	requestID := newRequestID()
	status, headers, body := provisioningEndpoint.exchange(t, http.MethodPut, orgPath, map[string]string{"X-Request-ID": requestID},
		map[string]any{"slug": "e2e-sync", "name": "Synchronized", "status": "active"})
	require.Equal(t, http.StatusOK, status, string(body))
	created := headers.Get("ETag")
	require.NotEmpty(t, created)

	status, headers, body = provisioningEndpoint.exchange(t, http.MethodGet, orgPath, nil, nil)
	require.Equal(t, http.StatusOK, status, string(body))
	require.Equal(t, created, headers.Get("ETag"))
	require.JSONEq(t, `{"slug":"e2e-sync","name":"Synchronized","status":"active"}`, string(body))

	renamed := map[string]any{"slug": "e2e-sync", "name": "Renamed", "status": "active"}
	status, headers, body = provisioningEndpoint.exchange(t, http.MethodPut, orgPath, map[string]string{"If-Match": created}, renamed)
	require.Equal(t, http.StatusOK, status, string(body))
	require.NotEqual(t, created, headers.Get("ETag"))
	status, _, body = provisioningEndpoint.exchange(t, http.MethodPut, orgPath, map[string]string{"If-Match": created}, renamed)
	require.Equal(t, http.StatusPreconditionFailed, status, string(body))
	require.Contains(t, string(body), "precondition_failed")

	// A change made outside the provisioning API is published as well.
	store := gormadapter.NewStore(db)
	var org gormadapter.Organization
	require.NoError(t, db.First(&org, "id = ?", strings.TrimPrefix(orgPath, "/v1/tenants/"+tenant.ID+"/organizations/")).Error)
	stored, err := store.GetOrgByID(t.Context(), model.OrgID(org.ID))
	require.NoError(t, err)
	require.NoError(t, store.SaveOrg(t.Context(), model.UpdateOrganization(stored, model.WithOrgName("Renamed locally"))))

	status, _, body = provisioningEndpoint.exchange(t, http.MethodGet, "/v1/events?cursor="+url.QueryEscape(start.Cursor), nil, nil)
	require.Equal(t, http.StatusOK, status, string(body))
	var page model.CommonEventPage
	require.NoError(t, json.Unmarshal(body, &page))
	var events []model.CommonEvent
	var types []string
	for _, event := range page.Items {
		if event.Data.Key.OrganizationID == org.ID {
			events = append(events, event)
			types = append(types, event.Type)
		}
	}
	require.Equal(t, []string{"organization.created.v1", "organization.updated.v1", "organization.updated.v1"}, types)
	require.Equal(t, requestID, events[0].RequestID)

	status, headers, _ = provisioningEndpoint.exchange(t, http.MethodGet, orgPath, nil, nil)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, events[len(events)-1].Data.ETag, headers.Get("ETag"))
}
