package provisionning

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	sloghttp "github.com/samber/slog-http"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// unauthorizedBody mirrors the error envelope of the v1 handler so machine
// clients only ever have to parse one shape. It is written verbatim here to
// keep the transport layer free of any dependency on a specific API version.
const unauthorizedBody = `{"error":{"code":"unauthorized","message":"a valid client certificate is required"}}`

// requireClientCert rejects any request that did not present a verified client
// certificate.
//
// The TLS handshake already enforces this when the server is configured by
// LoadTLSConfig. The check is kept as defense in depth so the handler can never
// be served in the clear by a misconfigured listener.
func requireClientCert(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			slog.WarnContext(r.Context(), "rejecting provisionning api request without client certificate",
				slog.String("remoteAddr", r.RemoteAddr), slog.String("path", r.URL.Path))

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(unauthorizedBody))

			return
		}

		identity := newClientIdentity(r.TLS.PeerCertificates[0])

		sloghttp.AddCustomAttributes(r, slog.String("clientCommonName", identity.CommonName))
		sloghttp.AddCustomAttributes(r, slog.String("clientSerialNumber", identity.SerialNumber))

		ctx := setClientIdentity(r.Context(), identity)
		uri := "urn:xolo:console:certificate:" + identity.SerialNumber
		if len(r.TLS.PeerCertificates[0].URIs) == 1 {
			uri = r.TLS.PeerCertificates[0].URIs[0].String()
		}
		ctx = model.WithActor(ctx, model.Actor{URI: uri, RequestID: uuid.NewString()})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
