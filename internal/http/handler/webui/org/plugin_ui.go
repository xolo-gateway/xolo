package org

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/pkg/errors"
)

// servePluginUI reverse-proxies requests to a plugin's embedded HTTP server.
// Route: /{orgSlug}/plugins/{pluginName}/ui/{uiPath...}
//
// Headers injected into every upstream request:
//   - X-Xolo-Org-Id        — org UUID, used by the plugin to scope GetConfig/SaveConfig calls
//   - X-Xolo-Plugin-Base-Path — absolute prefix under which the plugin UI is mounted in the app,
//     used by the plugin to construct correct relative URLs
//   - X-Xolo-User-Id       — the signed-in user, for plugins acting on their behalf
//   - X-Xolo-Public-Base-URL — public URL of the tenant, to build absolute URLs
//     (OAuth redirect URIs, links given to the user)
//   - X-Xolo-Node-Id       — pipeline node instance ID (from the ?nodeId= query param), used
//     by the plugin to scope GetSecret/SetSecret calls to this node placement
//
// Redirects emitted by the plugin are rewritten to stay under the plugin's base path.
func (h *Handler) servePluginUI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	pluginName := r.PathValue("pluginName")

	uiPort := h.pluginManager.HTTPPort(pluginName)
	if uiPort == 0 {
		http.NotFound(w, r)
		return
	}

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.NotFound(w, r)
		} else {
			slog.ErrorContext(ctx, "plugin_ui_proxy: org lookup failed",
				slog.String("slug", orgSlug),
				slog.Any("error", err),
			)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
		return
	}

	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", uiPort))
	proxy := httputil.NewSingleHostReverseProxy(target)

	pluginBasePath := fmt.Sprintf("/orgs/%s/plugins/%s/ui", orgSlug, pluginName)
	nodeID := r.URL.Query().Get("nodeId")

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// Inject org context so the plugin can identify which org is making the request.
		req.Header.Set("X-Xolo-Org-Id", string(org.ID()))
		// Inject the mount base path so the plugin can build correct relative/absolute URLs.
		req.Header.Set("X-Xolo-Plugin-Base-Path", pluginBasePath+"/")
		// The user and the public URL are always set, never forwarded from the
		// client: a plugin trusts them.
		req.Header.Del("X-Xolo-User-Id")
		if user := httpCtx.User(ctx); user != nil {
			req.Header.Set("X-Xolo-User-Id", string(user.ID()))
		}
		req.Header.Set("X-Xolo-Public-Base-URL", strings.TrimSuffix(httpCtx.BaseURL(ctx).String(), "/"))
		// Inject the node instance ID so the plugin can scope GetSecret/SetSecret calls.
		if nodeID != "" {
			req.Header.Set("X-Xolo-Node-Id", nodeID)
		}
		// Rewrite the path to the plugin's internal path space.
		uiPath := r.PathValue("uiPath")
		if uiPath == "" {
			uiPath = "/"
		} else if uiPath[0] != '/' {
			uiPath = "/" + uiPath
		}
		req.URL.Path = uiPath
		req.URL.RawPath = ""
	}

	// Rewrite absolute redirects emitted by the plugin so they stay under
	// the plugin's base path in the parent application.
	proxy.ModifyResponse = func(resp *http.Response) error {
		loc := resp.Header.Get("Location")
		if loc == "" {
			return nil
		}
		locURL, err := url.Parse(loc)
		if err != nil {
			return nil
		}
		if locURL.IsAbs() || locURL.Path == "" {
			return nil
		}
		if locURL.Path[0] == '/' {
			locURL.Path = pluginBasePath + locURL.Path
			resp.Header.Set("Location", locURL.String())
		}
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.ErrorContext(r.Context(), "plugin_ui_proxy: upstream error",
			slog.String("plugin", pluginName),
			slog.Any("error", err),
		)
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}
