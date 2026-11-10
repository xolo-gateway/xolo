package ratelimit

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/xolo-gateway/xolo/internal/metrics"
	"golang.org/x/time/rate"
)

// Middleware limits the request rate per client address. With trustHeaders,
// the address comes from X-Forwarded-For or X-Real-Ip (see clientAddr).
func Middleware(trustHeaders bool, trustedProxies []netip.Prefix, interval time.Duration, maxBurst int, cacheSize int, ttl time.Duration) func(http.Handler) http.Handler {
	cache := expirable.NewLRU[string, *rate.Limiter](cacheSize, nil, ttl)

	getLimiter := func(remoteAddr string) *rate.Limiter {
		limiter, exists := cache.Get(remoteAddr)
		if !exists {
			limiter = rate.NewLimiter(rate.Every(interval), maxBurst)
			cache.Add(remoteAddr, limiter)
		}

		return limiter
	}

	getRemoteAddr := func(r *http.Request) string {
		return clientAddr(r, trustHeaders, trustedProxies)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			remoteAddr := getRemoteAddr(r)
			limiter := getLimiter(remoteAddr)

			reservation := limiter.Reserve()
			if !reservation.OK() {
				metrics.RateLimitRejections.Inc()
				http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
				return
			}

			if reservation.Delay() > 0 {
				reservation.Cancel()

				metrics.RateLimitRejections.Inc()
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(reservation.Delay().Seconds()))))
				http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
				return
			}

			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(maxBurst))
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%.0f", limiter.Tokens()))
			tokens := limiter.Tokens()
			if tokens < float64(maxBurst) {
				secondsToReset := (float64(maxBurst) - tokens) / float64(maxBurst)
				resetTime := time.Now().Add(time.Duration(secondsToReset) * time.Second)
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))
			} else {
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Unix(), 10))
			}

			next.ServeHTTP(w, r)
		})
	}
}

// clientAddr returns the address a request is limited by.
//
// Without trusted proxies, the left-most X-Forwarded-For entry is taken, as
// before: it is the client's own claim, which any client can forge to get a
// fresh limit, but it keeps every client apart behind any number of proxies.
//
// With trusted proxies, the headers are only read when the connection comes
// from one of them, and X-Forwarded-For is walked from the right, where each
// proxy appends the address it received the request from: the first entry
// that is not a trusted proxy is the client, whatever it wrote to the left.
// X-Real-Ip is then ignored, since a proxy may pass the client's own on.
func clientAddr(r *http.Request, trustHeaders bool, trustedProxies []netip.Prefix) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}

	if !trustHeaders {
		return peer
	}

	if len(trustedProxies) == 0 {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
		if xri := r.Header.Get("X-Real-Ip"); xri != "" {
			return xri
		}
		return peer
	}

	trusted := func(addr string) bool {
		ip, err := netip.ParseAddr(strings.TrimSpace(addr))
		if err != nil {
			return false
		}
		ip = ip.Unmap()
		for _, prefix := range trustedProxies {
			if prefix.Contains(ip) {
				return true
			}
		}
		return false
	}

	if !trusted(peer) {
		return peer
	}

	// Every X-Forwarded-For header counts, in order: a proxy may add its own
	// header line instead of appending to the existing one.
	var hops []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		for _, hop := range strings.Split(line, ",") {
			if hop = strings.TrimSpace(hop); hop != "" {
				hops = append(hops, hop)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		if !trusted(hops[i]) {
			return hops[i]
		}
	}
	if len(hops) > 0 {
		// Every hop is a trusted proxy: the request started inside.
		return hops[0]
	}

	// X-Real-Ip is not read here: a proxy that does not rewrite it passes on
	// the client's own value. Trusted proxies append to X-Forwarded-For.
	return peer
}
