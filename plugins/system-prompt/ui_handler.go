package main

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
)

func newUIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("POST /api/config", handleSaveConfig)
	return mux
}

type uiPageData struct {
	BasePath string
	OrgID    string
	Config   Config
	Success  bool
}

func loadPageData(r *http.Request) uiPageData {
	ctx := r.Context()
	orgID := r.Header.Get("X-Xolo-Org-Id")
	basePath := r.Header.Get("X-Xolo-Plugin-Base-Path")
	if basePath == "" {
		basePath = "/"
	}
	host := pluginsdk.HostClientFromContext(ctx)
	pluginName := pluginsdk.PluginNameFromContext(ctx)

	var raw string
	if host != nil && orgID != "" {
		var err error
		raw, err = host.GetConfig(ctx, orgID, pluginName)
		if err != nil {
			slog.WarnContext(ctx, "system-prompt/ui: failed to load config", slog.Any("error", err))
		} else {
			slog.InfoContext(ctx, "system-prompt: config loaded",
				slog.String("org_id", orgID),
				slog.String("plugin_name", pluginName),
				slog.Int("raw_length", len(raw)),
			)
		}
	}

	cfg := parseConfig(raw)
	return uiPageData{BasePath: basePath, OrgID: orgID, Config: cfg}
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	pd := loadPageData(r)
	pd.Success = r.URL.Query().Get("saved") == "1"
	renderTempl(w, r, page(pd))
}

func handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := r.Header.Get("X-Xolo-Org-Id")
	host := pluginsdk.HostClientFromContext(ctx)
	pluginName := pluginsdk.PluginNameFromContext(ctx)

	if host == nil || orgID == "" {
		slog.WarnContext(ctx, "system-prompt: SaveConfig called without host or org context")
		http.Error(w, "missing host or org context", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		slog.WarnContext(ctx, "system-prompt/ui: failed to parse form body", slog.Any("error", err))
		http.Error(w, "invalid form body", http.StatusBadRequest)
		return
	}

	cfg := Config{
		SystemPrompt: r.FormValue("system_prompt"),
		Append:       r.FormValue("append") == "true",
	}

	b, _ := json.Marshal(cfg)
	if err := host.SaveConfig(ctx, orgID, pluginName, string(b)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.InfoContext(ctx, "system-prompt: config saved",
		slog.String("org_id", orgID),
		slog.Int("prompt_length", len(cfg.SystemPrompt)),
		slog.Bool("append", cfg.Append),
	)
	http.Redirect(w, r, "/?saved=1", http.StatusFound)
}

func renderTempl(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		slog.Error("system-prompt/ui: render error", slog.Any("error", err))
	}
}
