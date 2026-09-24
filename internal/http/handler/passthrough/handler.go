package passthrough

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"time"

	"github.com/pkg/errors"
)

// ProbeHandler answers the connectivity check an Anthropic-compatible client
// issues against its base URL before sending any request.
//
// It must be mounted *outside* the authentication chain: the probe carries no
// credentials at all, and a client that cannot complete it never proceeds to
// the actual API call — it simply hangs until its own timeout. The handler
// therefore reveals nothing beyond the fact that a relay is mounted.
func ProbeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	})
}

// Handler relays an authenticated request to the configured upstream using the
// credential the client supplied, and records the token usage the response
// reports.
//
// Xolo holds no credential for this path and makes no routing decision: the
// request body, model choice and upstream account are all the client's. The
// gateway's contribution is identity, authorization and metering.
type Handler struct {
	proxy        *httputil.ReverseProxy
	recorder     usageRecorder
	allowedPaths []string
}

// usageRecorder is the metering side of the relay, kept behind an interface so
// the transport can be tested without standing up the stores. *Recorder is the
// production implementation.
type usageRecorder interface {
	Record(ctx context.Context, id identity, modelName string, usage TokenUsage)
}

// NewHandler builds the relay.
//
// credentialHeader is the header carrying the Xolo token; the relay strips it on
// the way out so the gateway's own credential cannot reach the upstream even if
// the handler is mounted without CredentialSwap in front of it.
//
// upstreamTimeout is XOLO_PROXY_UPSTREAM_TIMEOUT, the same bound the proxied
// path uses. It caps the wait for the response headers, never the response
// itself: a relayed agentic call streams for as long as the upstream keeps
// talking. 0 disables it.
//
// ponytail: header timeout only. The proxied path also bounds the silence
// between two chunks (internal/adapter/proxy/timeout_client.go), which cannot be
// reused here — it wraps an llm.Client, not a RoundTripper. Add an idle-read
// wrapper around the response body if a stalled upstream mid-stream shows up in
// practice.
func NewHandler(upstreamBaseURL string, credentialHeader string, allowedPaths []string, upstreamTimeout time.Duration, recorder usageRecorder) (*Handler, error) {
	upstream, err := url.Parse(upstreamBaseURL)
	if err != nil {
		return nil, errors.Wrapf(err, "could not parse upstream base url %q", upstreamBaseURL)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = upstreamTimeout

	h := &Handler{
		recorder:     recorder,
		allowedPaths: slices.Clone(allowedPaths),
	}

	h.proxy = &httputil.ReverseProxy{
		Transport: transport,
		// FlushInterval -1 forwards every write immediately. Without it the
		// response is buffered and a streamed reply arrives all at once at the
		// end, which for an interactive client is indistinguishable from a hang.
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = upstream.Scheme
			pr.Out.URL.Host = upstream.Host
			pr.Out.URL.Path = upstream.Path + pr.In.URL.Path
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			// Host must track the upstream, not the gateway, or the upstream
			// routes on a name it does not serve.
			pr.Out.Host = upstream.Host

			// Put the client's own credential back. CredentialSwap replaced it
			// with the Xolo token so the authn chain could read it; from here on
			// only the upstream credential is relevant.
			if credential := UpstreamCredentialFromContext(pr.In.Context()); credential != "" {
				pr.Out.Header.Set("Authorization", credential)
			} else {
				pr.Out.Header.Del("Authorization")
			}

			// The Xolo token authenticates the caller to this gateway and means
			// nothing upstream; forwarding it would hand a third party a
			// credential to this instance.
			if credentialHeader != "" {
				pr.Out.Header.Del(credentialHeader)
			}

			// Forwarding headers would tell the upstream about the gateway's
			// network position without adding anything it acts on.
			pr.Out.Header.Del("X-Forwarded-For")
			pr.Out.Header.Del("X-Forwarded-Host")
			pr.Out.Header.Del("X-Forwarded-Proto")
		},
		ModifyResponse: h.sniffUsage,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.ErrorContext(r.Context(), "passthrough: upstream request failed",
				slog.Any("error", errors.WithStack(err)), slog.String("path", r.URL.Path))
			http.Error(w, "upstream request failed", http.StatusBadGateway)
		},
	}

	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !slices.Contains(h.allowedPaths, r.URL.Path) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}

	// Identity is resolved before the call, not in ModifyResponse: by the time
	// the response body closes, the request context may already be cancelled.
	id, ok := identityFromContext(r.Context())
	if !ok {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	if UpstreamCredentialFromContext(r.Context()) == "" {
		http.Error(w, "missing upstream credential in Authorization header", http.StatusBadRequest)
		return
	}

	h.proxy.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), id)))
}

// sniffUsage wraps the upstream body so token counters are extracted as the
// response streams to the client, without buffering it.
func (h *Handler) sniffUsage(res *http.Response) error {
	id, ok := identityFromRequestContext(res.Request.Context())
	if !ok {
		return nil
	}

	if res.StatusCode >= http.StatusBadRequest {
		// A failed call bills nothing upstream; recording it would inflate the
		// ledger with usage the operator was never charged for.
		return nil
	}

	// Recording runs on body close, once the client already has every byte, so
	// it costs the response nothing. The context is detached: it is cancelled
	// the moment the client disconnects, which would otherwise lose the usage
	// for exactly the long-running calls most worth metering.
	res.Body = newUsageSniffer(res.Body, res.Header.Get("Content-Type"), func(usage TokenUsage, modelName string) {
		h.recorder.Record(context.WithoutCancel(res.Request.Context()), id, modelName, usage)
	})

	return nil
}

const contextKeyIdentity contextKey = "identity"

func withIdentity(ctx context.Context, id identity) context.Context {
	return context.WithValue(ctx, contextKeyIdentity, id)
}

func identityFromRequestContext(ctx context.Context) (identity, bool) {
	id, ok := ctx.Value(contextKeyIdentity).(identity)
	return id, ok
}
