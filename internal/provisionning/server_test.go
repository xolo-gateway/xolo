package provisionning_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/provisionning"
)

func TestLoadTLSConfig(t *testing.T) {
	pki := newTestPKI(t, "xolo-test-ca")
	serverCert, serverKey := pki.issue(t, "server", true)

	t.Run("requires and verifies client certificates", func(t *testing.T) {
		tlsConfig, err := provisionning.LoadTLSConfig(serverCert, serverKey, pki.caCertFile, "urn:xolo:test:console")
		if err != nil {
			t.Fatalf("load tls config: %v", err)
		}

		if tlsConfig.ClientAuth != tls.RequireAndVerifyClientCert {
			t.Errorf("client auth: got %v, want %v", tlsConfig.ClientAuth, tls.RequireAndVerifyClientCert)
		}
		if tlsConfig.ClientCAs == nil {
			t.Error("client certificate authorities should be configured")
		}
		if tlsConfig.MinVersion < tls.VersionTLS13 {
			t.Errorf("min version: got %v, want at least TLS 1.3", tlsConfig.MinVersion)
		}
		if len(tlsConfig.Certificates) != 1 {
			t.Errorf("certificates: got %d, want 1", len(tlsConfig.Certificates))
		}
	})

	t.Run("refuses an incomplete or invalid configuration", func(t *testing.T) {
		emptyCA := filepath.Join(t.TempDir(), "empty.crt")
		if err := os.WriteFile(emptyCA, []byte("not a certificate"), 0o600); err != nil {
			t.Fatalf("write empty ca: %v", err)
		}

		missing := filepath.Join(t.TempDir(), "missing.pem")

		for name, args := range map[string][3]string{
			"no certificate": {"", serverKey, pki.caCertFile},
			"no key":         {serverCert, "", pki.caCertFile},
			"no ca":          {serverCert, serverKey, ""},
			"missing files":  {missing, missing, pki.caCertFile},
			"missing ca":     {serverCert, serverKey, missing},
			"mismatched key": {serverCert, mustIssueKey(t, pki), pki.caCertFile},
			"ca without pem": {serverCert, serverKey, emptyCA},
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := provisionning.LoadTLSConfig(args[0], args[1], args[2], "urn:xolo:test:console"); err == nil {
					t.Error("an error was expected")
				}
			})
		}
	})
}

// mustIssueKey returns the private key of an unrelated certificate, used to
// check that a key not matching the server certificate is refused.
func mustIssueKey(t *testing.T, pki *testPKI) string {
	t.Helper()
	_, key := pki.issue(t, "unrelated", true)
	return key
}

func TestServerMutualTLS(t *testing.T) {
	pki := newTestPKI(t, "xolo-test-ca")
	serverCert, serverKey := pki.issue(t, "server", true)
	clientCert, clientKey := pki.issue(t, "client", false)

	rogue := newTestPKI(t, "rogue-ca")
	rogueCert, rogueKey := rogue.issue(t, "rogue-client", false)

	tlsConfig, err := provisionning.LoadTLSConfig(serverCert, serverKey, pki.caCertFile, "urn:xolo:test:console")
	if err != nil {
		t.Fatalf("load tls config: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := provisionning.CurrentClientIdentity(r.Context())
		if !ok {
			t.Error("client identity should be available in the request context")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if authority := model.WriteAuthority(r.Context()); authority != model.OwnerControlPlane {
			t.Errorf("write authority: got %q, want control_plane", authority)
		}
		_, _ = io.WriteString(w, identity.CommonName)
	})

	baseURL := startServer(t, tlsConfig, handler)

	t.Run("accepts a certificate signed by the configured authority", func(t *testing.T) {
		client := newTLSClient(t, pki.caCertPEM, clientCert, clientKey)

		res, err := client.Get(baseURL + "/whoami")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Errorf("status: got %d, want %d", res.StatusCode, http.StatusOK)
		}

		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != "client" {
			t.Errorf("common name: got %q, want %q", string(body), "client")
		}
	})

	t.Run("refuses a connection without client certificate", func(t *testing.T) {
		client := newTLSClient(t, pki.caCertPEM, "", "")

		res, err := client.Get(baseURL + "/whoami")
		if err == nil {
			res.Body.Close()
			t.Fatalf("request should have failed, got status %d", res.StatusCode)
		}
		if !strings.Contains(err.Error(), "certificate required") && !strings.Contains(err.Error(), "bad certificate") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("refuses a certificate signed by an untrusted authority", func(t *testing.T) {
		client := newTLSClient(t, pki.caCertPEM, rogueCert, rogueKey)

		res, err := client.Get(baseURL + "/whoami")
		if err == nil {
			res.Body.Close()
			t.Fatalf("request should have failed, got status %d", res.StatusCode)
		}
	})
}

func TestServerRequiresConfiguration(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	if err := provisionning.NewServer(provisionning.WithHandler(handler)).Run(context.Background()); err == nil {
		t.Error("a server without TLS configuration should refuse to run")
	}

	pki := newTestPKI(t, "xolo-test-ca")
	serverCert, serverKey := pki.issue(t, "server", true)
	tlsConfig, err := provisionning.LoadTLSConfig(serverCert, serverKey, pki.caCertFile, "urn:xolo:test:console")
	if err != nil {
		t.Fatalf("load tls config: %v", err)
	}

	if err := provisionning.NewServer(provisionning.WithTLSConfig(tlsConfig)).Run(context.Background()); err == nil {
		t.Error("a server without handler should refuse to run")
	}
}

// startServer runs a Provisionning API server on an ephemeral port and returns its
// base URL. It is stopped when the test ends.
func startServer(t *testing.T, tlsConfig *tls.Config, handler http.Handler) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := provisionning.NewServer(
		provisionning.WithTLSConfig(tlsConfig),
		provisionning.WithClientPolicy([]string{"urn:xolo:test:console"}, 100, 100),
		provisionning.WithHandler(handler),
		provisionning.WithListener(listener),
		provisionning.WithShutdownTimeout(time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop in time")
		}
	})

	return "https://" + listener.Addr().String()
}

func newTLSClient(t *testing.T, caPEM []byte, certFile, keyFile string) *http.Client {
	t.Helper()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("could not append ca certificate")
	}

	tlsConfig := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}

	if certFile != "" && keyFile != "" {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			t.Fatalf("load client key pair: %v", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}

	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsConfig},
	}
}

func TestRejectedCertificateURIs(t *testing.T) {
	pki := newTestPKI(t, "authority")
	serverCert, serverKey := pki.issue(t, "server", true)
	cfg, err := provisionning.LoadTLSConfig(serverCert, serverKey, pki.caCertFile, "urn:xolo:test:console")
	require.NoError(t, err)
	address := startServer(t, cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for name, customize := range map[string]func(*x509.Certificate){
		"no URI":      func(c *x509.Certificate) { c.URIs = nil },
		"two URIs":    func(c *x509.Certificate) { c.URIs = append(c.URIs, c.URIs[0]) },
		"unknown URI": func(c *x509.Certificate) { u, _ := url.Parse("urn:unknown"); c.URIs = []*url.URL{u} },
		"expired":     func(c *x509.Certificate) { c.NotAfter = time.Now().Add(-time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			cert, key := pki.issue(t, name, false, customize)
			client := newTLSClient(t, pki.caCertPEM, cert, key)
			resp, err := client.Get(address + "/v1/healthz")
			if resp != nil {
				resp.Body.Close()
			}
			require.Error(t, err)
		})
	}
	cert, key := pki.issue(t, "tls12", false)
	client := newTLSClient(t, pki.caCertPEM, cert, key)
	client.Transport.(*http.Transport).TLSClientConfig.MinVersion = tls.VersionTLS12
	client.Transport.(*http.Transport).TLSClientConfig.MaxVersion = tls.VersionTLS12
	resp, err := client.Get(address + "/v1/healthz")
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err)
}

func TestCertificateRenewalPreservesURI(t *testing.T) {
	pki := newTestPKI(t, "renewal-ca")
	cert, key := pki.issue(t, "server", true)
	config, err := provisionning.LoadTLSConfig(cert, key, pki.caCertFile, "urn:xolo:test:console")
	require.NoError(t, err)
	address := startServer(t, config, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := provisionning.CurrentClientIdentity(r.Context())
		require.True(t, ok)
		_, _ = io.WriteString(w, identity.URI)
	}))
	for _, cn := range []string{"original", "renewed-with-different-cn"} {
		t.Run(cn, func(t *testing.T) {
			cert, key := pki.issue(t, cn, false)
			client := newTLSClient(t, pki.caCertPEM, cert, key)
			resp, err := client.Get(address + "/v1/healthz")
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, "urn:xolo:test:console", string(body))
		})
	}
}
