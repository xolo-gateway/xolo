package provisionning

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

type Options struct {
	AuthorizedURIs    []string
	RequestsPerSecond float64
	Burst             int
	// Address is the listen address used when no Listener is provided.
	Address string

	// TLSConfig carries the server certificate and the client certificate
	// verification policy. It is required: the Provisionning API never serves plain HTTP.
	TLSConfig *tls.Config

	// Handler serves the API routes. It is mounted behind the client
	// certificate check.
	Handler http.Handler

	// Listener, when set, is served instead of dialing Address. Tests use it to
	// bind an ephemeral port.
	Listener net.Listener

	// ShutdownTimeout bounds the graceful shutdown triggered by context
	// cancellation.
	ShutdownTimeout time.Duration
}

type OptionFunc func(*Options)

func NewOptions(funcs ...OptionFunc) *Options {
	opts := &Options{
		Address:           ":3003",
		RequestsPerSecond: 10, Burst: 20,
		ShutdownTimeout: 10 * time.Second,
	}

	for _, fn := range funcs {
		fn(opts)
	}

	return opts
}

func WithAddress(address string) OptionFunc {
	return func(opts *Options) {
		opts.Address = address
	}
}

func WithTLSConfig(tlsConfig *tls.Config) OptionFunc {
	return func(opts *Options) {
		opts.TLSConfig = tlsConfig
	}
}

func WithHandler(handler http.Handler) OptionFunc {
	return func(opts *Options) {
		opts.Handler = handler
	}
}

func WithListener(listener net.Listener) OptionFunc {
	return func(opts *Options) {
		opts.Listener = listener
	}
}

func WithShutdownTimeout(timeout time.Duration) OptionFunc {
	return func(opts *Options) {
		opts.ShutdownTimeout = timeout
	}
}

func WithClientPolicy(uris []string, requestsPerSecond float64, burst int) OptionFunc {
	return func(o *Options) {
		o.AuthorizedURIs = append([]string(nil), uris...)
		o.RequestsPerSecond = requestsPerSecond
		o.Burst = burst
	}
}
