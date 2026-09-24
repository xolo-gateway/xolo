package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
)

func newUIHandler(oauth *oauthClient) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("POST /api/config", handleSaveConfig)
	mux.HandleFunc("GET /connect", handleConnect)
	mux.HandleFunc("POST /connect", oauth.handleStartConnect)
	mux.HandleFunc("GET /callback", oauth.handleCallback)
	return mux
}

type uiPageData struct {
	BasePath     string
	OrgID        string
	NodeID       string
	Config       Config
	HasAuthValue bool
	Success      bool
	Error        string
}

func loadPageData(r *http.Request) (uiPageData, error) {
	ctx := r.Context()
	orgID := r.Header.Get("X-Xolo-Org-Id")
	nodeID := r.Header.Get("X-Xolo-Node-Id")
	basePath := r.Header.Get("X-Xolo-Plugin-Base-Path")
	if basePath == "" {
		basePath = "/"
	}

	host := pluginsdk.HostClientFromContext(ctx)
	pluginName := pluginsdk.PluginNameFromContext(ctx)

	var raw string
	var hasAuthValue bool
	if host != nil && orgID != "" {
		var err error
		raw, err = host.GetConfig(ctx, orgID, pluginName)
		if err != nil {
			slog.WarnContext(ctx, "mcp-bridge/ui: failed to load config", slog.Any("error", err))
		}
		if nodeID != "" {
			_, found, err := host.GetSecret(ctx, orgID, pluginName, nodeID, secretKeyAuthValue)
			if err != nil {
				slog.WarnContext(ctx, "mcp-bridge/ui: failed to check auth secret", slog.Any("error", err))
			}
			hasAuthValue = found
		}
	}

	cfg, err := parseConfig(raw)
	if err != nil {
		cfg = Config{}
	}

	return uiPageData{
		BasePath:     basePath,
		OrgID:        orgID,
		NodeID:       nodeID,
		Config:       cfg,
		HasAuthValue: hasAuthValue,
	}, nil
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	pd, err := loadPageData(r)
	if err != nil {
		httpError(w, err)
		return
	}
	pd.Success = r.URL.Query().Get("saved") == "1"
	renderTempl(w, r, page(pd))
}

func handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := r.Header.Get("X-Xolo-Org-Id")
	nodeID := r.Header.Get("X-Xolo-Node-Id")
	host := pluginsdk.HostClientFromContext(ctx)
	pluginName := pluginsdk.PluginNameFromContext(ctx)

	if host == nil || orgID == "" {
		http.Error(w, "missing host or org context", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The form action is a relative URL, so the browser does not carry over
	// the page's "?nodeId=" query string on submission (unlike X-Xolo-Org-Id,
	// which is derived from the URL path and stays present on every proxied
	// request). Fall back to the hidden form field set by page.templ.
	if nodeID == "" {
		nodeID = r.FormValue("nodeId")
	}

	timeoutSeconds, _ := strconv.Atoi(r.FormValue("timeout_seconds"))
	maxConsecutiveToolCalls, _ := strconv.Atoi(r.FormValue("max_consecutive_tool_calls"))
	cfg := Config{
		Endpoint:                strings.TrimSpace(r.FormValue("endpoint")),
		AuthMode:                r.FormValue("auth_mode"),
		PublicBaseURL:           r.Header.Get("X-Xolo-Public-Base-URL"),
		AuthHeaderName:          strings.TrimSpace(r.FormValue("auth_header_name")),
		ToolFilter:              splitNonEmpty(r.FormValue("tool_filter")),
		TimeoutSeconds:          timeoutSeconds,
		MaxConsecutiveToolCalls: maxConsecutiveToolCalls,
	}

	if cfg.AuthMode != AuthModeOAuth {
		cfg.AuthMode = AuthModeStatic
	}

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := host.SaveConfig(ctx, orgID, pluginName, string(cfgJSON)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// The auth value is a secret: stored via SetSecret (per node instance),
	// never persisted alongside the visible config JSON above. Empty input
	// leaves the previously stored value untouched.
	if authValue := r.FormValue("auth_value"); authValue != "" {
		if nodeID == "" {
			http.Error(w, "missing node context", http.StatusBadRequest)
			return
		}
		if err := host.SetSecret(ctx, orgID, pluginName, nodeID, secretKeyAuthValue, authValue); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, "/?saved=1", http.StatusFound)
}

func authValuePlaceholder(hasValue bool) string {
	if hasValue {
		return "••••••••"
	}
	return ""
}

// zeroAsEmpty renders 0 as an empty string so the input shows its
// placeholder (the host-applied default) instead of a literal "0".
func zeroAsEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func splitNonEmpty(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func httpError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func renderTempl(w http.ResponseWriter, r *http.Request, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := component.Render(r.Context(), w); err != nil {
		slog.Error("mcp-bridge/ui: template render error", slog.Any("error", err))
	}
}
