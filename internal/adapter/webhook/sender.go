// Package webhook delivers signed events over HTTPS. Destinations are bounded
// by an operator allowlist and every resolved address is checked before it is
// dialled. Credentials and response contents never enter diagnostics.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// MaxResponseBytes bounds what is read of a response before it is discarded.
const MaxResponseBytes = 64 << 10

const RequestTimeout = 10 * time.Second

// prohibitedPrefixes are shared, reserved and special-use ranges, never
// public webhook targets, whatever the private network setting.
var prohibitedPrefixes = func() []netip.Prefix {
	var prefixes []netip.Prefix
	for _, raw := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"::/96", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48", "fec0::/10",
	} {
		prefixes = append(prefixes, netip.MustParsePrefix(raw))
	}
	return prefixes
}()

type Sender struct {
	origins  map[string]bool
	private  bool
	client   *http.Client
	resolver *net.Resolver
}

// NewSender accepts destinations on the given HTTPS origins only. roots nil
// means the system trust store.
func NewSender(origins []string, allowPrivate bool, roots *x509.CertPool) (*Sender, error) {
	s := &Sender{origins: map[string]bool{}, private: allowPrivate, resolver: net.DefaultResolver}
	for _, raw := range origins {
		u, err := destination(raw)
		if err != nil || (u.Path != "" && u.Path != "/") {
			return nil, fmt.Errorf("webhook origins must be HTTPS origins without paths")
		}
		s.origins[origin(u)] = true
	}
	if len(s.origins) == 0 {
		return nil, fmt.Errorf("webhook destination allowlist is required")
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            s.dialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		IdleConnTimeout:        30 * time.Second,
		MaxIdleConns:           10,
		MaxIdleConnsPerHost:    2,
		MaxConnsPerHost:        4,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 16 << 10,
	}
	s.client = &http.Client{Transport: transport, Timeout: RequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return s, nil
}

func destination(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(u.Hostname(), "*%") {
		return nil, fmt.Errorf("invalid webhook HTTPS destination")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid webhook port")
		}
	}
	return u, nil
}

func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// ValidateDestination implements port.WebhookSender. It does not resolve
// names: resolved addresses are checked when dialling.
func (s *Sender) ValidateDestination(raw string) error {
	u, err := destination(raw)
	if err != nil {
		return err
	}
	if !s.origins[origin(u)] {
		return fmt.Errorf("webhook destination is not allowlisted")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !allowedAddress(ip, s.private) {
		return fmt.Errorf("webhook address is prohibited")
	}
	return nil
}

// allowedAddress refuses link-local addresses, which include the cloud
// metadata endpoints, even when private networks are allowed.
func allowedAddress(ip netip.Addr, private bool) bool {
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return private
	}
	for _, prefix := range prohibitedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return ip.IsGlobalUnicast()
}

// dialContext checks every resolved address, then dials the checked
// addresses: a second resolution could rebind the name in between.
func (s *Sender) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid destination")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := s.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("destination resolution failed")
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no destination addresses")
	}
	for _, ip := range ips {
		if !allowedAddress(ip, s.private) {
			return nil, fmt.Errorf("destination network prohibited")
		}
	}
	for _, ip := range ips {
		conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("destination connection failed")
}

// DecodeSecret decodes a Standard Webhooks secret: optional whsec_ prefix,
// strict base64 of 32 to 64 bytes.
func DecodeSecret(secret string) ([]byte, error) {
	if len(secret) > 94 || strings.ContainsAny(secret, "\r\n") {
		return nil, fmt.Errorf("invalid webhook secret encoding")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil || len(raw) < 32 || len(raw) > 64 {
		return nil, fmt.Errorf("webhook secret must contain 32 to 64 base64-encoded bytes")
	}
	return raw, nil
}

// Sign computes the Standard Webhooks signature header: one v1 signature per
// secret, two during a rotation.
func Sign(id, timestamp string, body []byte, secrets []string) (string, error) {
	if len(secrets) == 0 || len(secrets) > 2 {
		return "", fmt.Errorf("one or two signing secrets required")
	}
	signatures := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		key, err := DecodeSecret(secret)
		if err != nil {
			return "", err
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + timestamp + "."))
		mac.Write(body)
		signatures = append(signatures, "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	}
	return strings.Join(signatures, " "), nil
}

// SendWebhook implements port.WebhookSender. A 2xx is an acknowledgement
// whatever the response body, which is read up to MaxResponseBytes and
// discarded.
func (s *Sender) SendWebhook(ctx context.Context, job *model.WebhookJob, secrets []string) model.WebhookResult {
	if err := s.ValidateDestination(job.Destination); err != nil {
		return model.WebhookResult{Diagnostic: "destination_rejected"}
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	signature, err := Sign(job.EventID, timestamp, []byte(job.Body), secrets)
	if err != nil {
		return model.WebhookResult{Diagnostic: "credentials_unavailable"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.Destination, strings.NewReader(job.Body))
	if err != nil {
		return model.WebhookResult{Diagnostic: "destination_rejected"}
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	req.Header.Set("webhook-id", job.EventID)
	req.Header.Set("webhook-timestamp", timestamp)
	req.Header.Set("webhook-signature", signature)
	resp, err := s.client.Do(req)
	if err != nil {
		return model.WebhookResult{Diagnostic: "transport_error"}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxResponseBytes))
	result := model.WebhookResult{StatusCode: resp.StatusCode, Diagnostic: "http_status"}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		result.Success, result.Diagnostic = true, "accepted"
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		result.Diagnostic = "redirect"
	}
	return result
}

// Close releases the idle connections.
func (s *Sender) Close() { s.client.CloseIdleConnections() }

var _ port.WebhookSender = (*Sender)(nil)
