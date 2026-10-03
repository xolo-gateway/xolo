package provisionning

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"time"

	sloghttp "github.com/samber/slog-http"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"golang.org/x/time/rate"
)

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func requestCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("X-Request-ID")
		id := ""
		if len(values) == 1 && requestIDPattern.MatchString(values[0]) {
			id = values[0]
		} else {
			var raw [16]byte
			_, _ = rand.Read(raw[:])
			id = hex.EncodeToString(raw[:])
		}
		// Downstream access logging must never observe an invalid supplied value.
		r = r.Clone(r.Context())
		r.Header.Set("X-Request-ID", id)
		w.Header().Set("X-Request-ID", id)
		ctx := model.WithActor(r.Context(), model.Actor{RequestID: id})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func transportError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func authorizedURI(state *tls.ConnectionState, allowed map[string]bool) (string, bool) {
	if state == nil || state.Version < tls.VersionTLS13 || len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 || len(state.PeerCertificates) == 0 {
		return "", false
	}
	leaf := state.PeerCertificates[0]
	if !leaf.Equal(state.VerifiedChains[0][0]) || len(leaf.URIs) != 1 {
		return "", false
	}
	uri := leaf.URIs[0].String()
	return uri, allowed[uri]
}

// Budgets are allocated only for configured identities: arbitrary certificates
// cannot grow the map. Each limiter is concurrency-safe and local to this server.
func requireClientCert(next http.Handler, uris []string, requestsPerSecond float64, burst int) http.Handler {
	allowed := map[string]bool{}
	budgets := map[string]*rate.Limiter{}
	for _, uri := range uris {
		allowed[uri] = true
		budgets[uri] = rate.NewLimiter(rate.Limit(requestsPerSecond), burst)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uri, ok := authorizedURI(r.TLS, allowed)
		if !ok {
			transportError(w, 403, "client_certificate_rejected", "client certificate rejected")
			return
		}
		actor := model.ActorFromContext(r.Context())
		actor.URI = uri
		ctx := model.WithActor(r.Context(), actor)
		sloghttp.AddCustomAttributes(r, slog.String("clientURI", uri))
		sloghttp.AddCustomAttributes(r, slog.String("requestID", actor.RequestID))
		identity := newClientIdentity(r.TLS.PeerCertificates[0])
		identity.URI = uri
		ctx = setClientIdentity(ctx, identity)
		now := time.Now()
		reservation := budgets[uri].ReserveN(now, 1)
		if delay := reservation.DelayFrom(now); delay > 0 || !reservation.OK() {
			reservation.CancelAt(now)
			seconds := int(math.Max(1, math.Ceil(delay.Seconds())))
			w.Header().Set("Retry-After", fmt.Sprint(seconds))
			transportError(w, 429, "rate_limited", "request rate exceeded")
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
