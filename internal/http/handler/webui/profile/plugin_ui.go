package profile

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// servePersonalPluginUI reverse-proxies plugin UI requests in the personal (no-org) context.
// Route: /plugins/{pluginName}/ui/{uiPath...}  (mounted under /profile/)
// Full URL in browser: /profile/plugins/{pluginName}/ui/...
func (h *Handler) servePersonalPluginUI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pluginName := r.PathValue("pluginName")

	if h.pluginManager == nil {
		http.NotFound(w, r)
		return
	}

	uiPort := h.pluginManager.HTTPPort(pluginName)
	if uiPort == 0 {
		http.NotFound(w, r)
		return
	}

	user := httpCtx.User(ctx)
	scopeID := ""
	if user != nil {
		scopeID = "~:" + string(user.ID())
	}

	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", uiPort))
	proxy := httputil.NewSingleHostReverseProxy(target)

	pluginBasePath := fmt.Sprintf("/profile/plugins/%s/ui", pluginName)

	nodeID := r.URL.Query().Get("nodeId")

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Header.Set("X-Xolo-Org-Id", scopeID)
		req.Header.Set("X-Xolo-Plugin-Base-Path", pluginBasePath+"/")
		// The user and the public URL are always set, never forwarded from
		// the client: a plugin trusts them.
		req.Header.Del("X-Xolo-User-Id")
		if user != nil {
			req.Header.Set("X-Xolo-User-Id", string(user.ID()))
		}
		req.Header.Set("X-Xolo-Public-Base-URL", strings.TrimSuffix(httpCtx.BaseURL(ctx).String(), "/"))
		if nodeID != "" {
			req.Header.Set("X-Xolo-Node-Id", nodeID)
		}
		uiPath := r.PathValue("uiPath")
		if uiPath == "" {
			uiPath = "/"
		} else if uiPath[0] != '/' {
			uiPath = "/" + uiPath
		}
		req.URL.Path = uiPath
		req.URL.RawPath = ""
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		loc := resp.Header.Get("Location")
		if loc == "" {
			return nil
		}
		locURL, err := url.Parse(loc)
		if err != nil || locURL.IsAbs() || locURL.Path == "" {
			return nil
		}
		if locURL.Path[0] == '/' {
			locURL.Path = pluginBasePath + locURL.Path
			resp.Header.Set("Location", locURL.String())
		}
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.ErrorContext(r.Context(), "personal_plugin_ui_proxy: upstream error",
			slog.String("plugin", pluginName),
			slog.Any("error", err),
		)
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}
