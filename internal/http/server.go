package http

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"github.com/rs/cors"
	sloghttp "github.com/samber/slog-http"

	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/colorscheme"
	"github.com/xolo-gateway/xolo/internal/http/middleware/httpmetrics"
)

type Server struct {
	opts *Options
}

func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	mux := &http.ServeMux{}
	for mountpoint, handler := range s.opts.Mounts {
		mount(mux, mountpoint, handler)
	}
	for pattern, handler := range s.opts.Routes {
		mux.Handle(pattern, handler)
	}

	handler := sloghttp.Recovery(mux)

	// Applied in reverse so the first declared middleware ends up outermost.
	// They sit inside the request-scoped context injection below: tenant
	// resolution renders an error page when it fails, and those templates read
	// the base and current URLs.
	for i := len(s.opts.Middlewares) - 1; i >= 0; i-- {
		handler = s.opts.Middlewares[i](handler)
	}

	handler = sloghttp.New(slog.Default())(handler)
	handler = httpmetrics.Middleware()(handler)

	cors := cors.New(s.opts.CORS)

	handler = cors.Handler(handler)

	handler = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			ctx = httpCtx.SetBaseURL(ctx, s.opts.BaseURL)
			ctx = httpCtx.SetCurrentURL(ctx, r.URL)

			// Le repli de la barre latérale est un choix de l'utilisateur, écrit
			// en cookie par sidebar.min.js. La coquille n'étant rendue qu'au
			// chargement complet, c'est ici qu'il faut le relire — sinon chaque
			// rafraîchissement rouvre la barre.
			if cookie, err := r.Cookie(httpCtx.SidebarCookie); err == nil {
				ctx = httpCtx.SetSidebarExpanded(ctx, cookie.Value != "false")
			}

			r = r.WithContext(ctx)

			next.ServeHTTP(w, r)
		})
	}(handler)

	colorScheme := colorscheme.Middleware()

	handler = colorScheme(handler)

	server := http.Server{
		Addr:    s.opts.Address,
		Handler: handler,
	}

	go func() {
		<-ctx.Done()
		if err := server.Close(); err != nil {
			slog.ErrorContext(ctx, "could not close server", slog.Any("error", errors.WithStack(err)))
		}
	}()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.WithStack(err)
	}

	return nil
}

func mount(mux *http.ServeMux, prefix string, handler http.Handler) {
	trimmed := strings.TrimSuffix(prefix, "/")

	if len(trimmed) > 0 {
		mux.Handle(prefix, http.StripPrefix(trimmed, handler))
	} else {
		mux.Handle(prefix, handler)
	}
}

func NewServer(funcs ...OptionFunc) *Server {
	opts := NewOptions(funcs...)
	return &Server{
		opts: opts,
	}
}
