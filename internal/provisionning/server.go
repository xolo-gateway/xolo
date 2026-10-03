package provisionning

import (
	"context"
	"crypto/tls"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	sloghttp "github.com/samber/slog-http"
)

// Server exposes the Provisionning API on its own listener, with its own TLS
// configuration and its own middleware chain.
//
// It deliberately shares nothing with the public HTTP server beyond the process
// and its root context: no CORS, no cookie, no color scheme, no session, no
// user authentication. Its only authentication mechanism is the client
// certificate verified during the TLS handshake.
type Server struct {
	opts *Options
}

func NewServer(funcs ...OptionFunc) *Server {
	return &Server{
		opts: NewOptions(funcs...),
	}
}

// Addr returns the address the server listens on.
func (s *Server) Addr() string {
	if s.opts.Listener != nil {
		return s.opts.Listener.Addr().String()
	}
	return s.opts.Address
}

// Run serves the API until ctx is canceled, then shuts down gracefully within
// the configured timeout.
func (s *Server) Run(ctx context.Context) error {
	if s.opts.TLSConfig == nil {
		return errors.New("provisionning api server requires a TLS configuration")
	}
	if s.opts.Handler == nil {
		return errors.New("provisionning api server requires a handler")
	}

	if len(s.opts.AuthorizedURIs) == 0 || s.opts.RequestsPerSecond <= 0 || math.IsNaN(s.opts.RequestsPerSecond) || math.IsInf(s.opts.RequestsPerSecond, 0) || s.opts.Burst < 1 {
		return errors.New("invalid provisioning client authorization or rate limits")
	}
	if s.opts.TLSConfig.MinVersion < tls.VersionTLS13 || s.opts.TLSConfig.ClientAuth != tls.RequireAndVerifyClientCert || s.opts.TLSConfig.ClientCAs == nil {
		return errors.New("provisioning requires TLS 1.3 and verified client certificates")
	}
	handler := requireClientCert(s.opts.Handler, s.opts.AuthorizedURIs, s.opts.RequestsPerSecond, s.opts.Burst)
	handler = sloghttp.Recovery(handler)
	handler = sloghttp.New(slog.Default())(handler)
	handler = requestCorrelation(handler)

	server := &http.Server{
		Addr:              s.opts.Address,
		Handler:           handler,
		TLSConfig:         s.opts.TLSConfig,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	shutdownDone := make(chan struct{})
	// serveDone releases the watcher when serving stops on its own — a failed
	// bind, an unreadable key — instead of leaving it parked on ctx for the
	// lifetime of whoever embeds this server.
	serveDone := make(chan struct{})
	defer close(serveDone)

	go func() {
		defer close(shutdownDone)

		select {
		case <-ctx.Done():
		case <-serveDone:
			return
		}

		// Detached from ctx, which is already canceled, so in-flight requests
		// keep the timeout they are entitled to.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.ShutdownTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.ErrorContext(ctx, "could not gracefully shutdown provisionning api server", slogx.Error(errors.WithStack(err)))
		}
	}()

	// The certificate and key already live in TLSConfig, hence the empty file
	// names.
	var err error
	if s.opts.Listener != nil {
		err = server.ServeTLS(s.opts.Listener, "", "")
	} else {
		err = server.ListenAndServeTLS("", "")
	}

	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.WithStack(err)
	}

	if ctx.Err() != nil {
		<-shutdownDone
	}

	return nil
}
