// Package webhook implements the bounded HTTPS delivery adapter. Application
// credentials and arbitrary response/error strings never enter diagnostics.
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
)

const MaxResponseBytes = 64 << 10
const RequestTimeout = 10 * time.Second

type Sender struct {
	origins  map[string]bool
	private  bool
	client   *http.Client
	resolver *net.Resolver
}

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
	tr := &http.Transport{Proxy: nil, DialContext: s.dialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 10, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4, DisableCompression: true, MaxResponseHeaderBytes: 16 << 10}
	s.client = &http.Client{Transport: tr, Timeout: RequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return s, nil
}
func destination(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(u.Hostname(), "*") || u.Fragment != "" || u.Opaque != "" || strings.Contains(u.Hostname(), "%") {
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
func allowedAddress(ip netip.Addr, private bool) bool {
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return private
	}
	// Shared, reserved and special-use ranges are not public webhook targets.
	for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "::/96", "2001::/23", "2001:db8::/32", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16"} {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	return ip.IsGlobalUnicast()
}
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
	// Validate every answer then dial the checked IP, avoiding a second DNS
	// resolution (and DNS rebinding between validation and connect).
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
func Sign(id, timestamp string, body []byte, secrets []string) (string, error) {
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
	if len(signatures) == 0 || len(signatures) > 2 {
		return "", fmt.Errorf("one or two signing secrets required")
	}
	return strings.Join(signatures, " "), nil
}
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
	result := model.WebhookResult{StatusCode: resp.StatusCode, Diagnostic: "http_status"}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, MaxResponseBytes+1))
	if n > MaxResponseBytes {
		result.Diagnostic = "response_too_large"
		return result
	}
	if err != nil {
		result.Diagnostic = "transport_error"
		return result
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		result.Diagnostic = "redirect"
		return result
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		result.Success = true
		result.Diagnostic = "accepted"
	}
	return result
}
func (s *Sender) Close() { s.client.CloseIdleConnections() }
