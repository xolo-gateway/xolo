package model

import (
	"strings"

	"golang.org/x/net/http/httpguts"
)

// ProviderRequestHeaders configures headers sent with every request to a
// provider, for gateways that require them. OpenCode Go, for one, refuses a
// request without a session header and asks clients to send their own
// User-Agent.
type ProviderRequestHeaders struct {
	// Headers are added to every request, replacing the SDK's own. They
	// are configuration, not secrets: the API key is the only secret.
	Headers map[string]string `json:"headers,omitempty"`
	// SessionHeader names the header carrying the ID of the conversation a
	// request belongs to: the client's own when it sends one, a fingerprint
	// of the conversation otherwise.
	SessionHeader string `json:"session_header,omitempty"`
}

const (
	maxProviderHeaders     = 20
	maxProviderHeaderName  = 100
	maxProviderHeaderValue = 1000
)

// reservedProviderHeaders cannot be configured: they carry credentials,
// frame the request, or claim where it comes from.
var reservedProviderHeaders = []string{
	"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Proxy-Authorization", "Cookie",
	"Host", "Content-Length", "Content-Type", "Transfer-Encoding", "Connection",
	"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip",
}

// Empty reports whether h configures nothing.
func (h *ProviderRequestHeaders) Empty() bool {
	return h == nil || (len(h.Headers) == 0 && h.SessionHeader == "")
}

// Valid reports whether every name is a header name that may be
// configured and every value a valid, non-empty header value, within bounds.
func (h *ProviderRequestHeaders) Valid() bool {
	if h == nil {
		return true
	}
	if len(h.Headers) > maxProviderHeaders {
		return false
	}
	for name, value := range h.Headers {
		if !validProviderHeaderName(name) || value == "" || len(value) > maxProviderHeaderValue || !httpguts.ValidHeaderFieldValue(value) {
			return false
		}
	}
	if h.SessionHeader != "" && !validProviderHeaderName(h.SessionHeader) {
		return false
	}
	return true
}

func validProviderHeaderName(name string) bool {
	if len(name) > maxProviderHeaderName || !httpguts.ValidHeaderFieldName(name) {
		return false
	}
	for _, reserved := range reservedProviderHeaders {
		if strings.EqualFold(name, reserved) {
			return false
		}
	}
	return true
}
