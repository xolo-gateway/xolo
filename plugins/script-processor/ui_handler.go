package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	"github.com/xolo-gateway/xolo/plugins/internal/uilocale"
)

func newUIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("POST /api/config", handleSaveConfig)
	mux.HandleFunc("POST /api/inputs/add", handleAddInput)
	mux.HandleFunc("POST /api/inputs/{idx}/delete", handleDeleteInput)
	mux.HandleFunc("POST /api/outputs/add", handleAddOutput)
	mux.HandleFunc("POST /api/outputs/{idx}/delete", handleDeleteOutput)
	return uilocale.Handler(mux)
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
			slog.WarnContext(ctx, "script-processor/ui: failed to load config", slog.Any("error", err))
		}
	}

	cfg, _ := parseConfig(raw)
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
		http.Error(w, "missing host or org context", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cfg := Config{
		Script:  strings.TrimSpace(r.FormValue("script")),
		Inputs:  parsePortsFromForm(r, "input"),
		Outputs: parsePortsFromForm(r, "output"),
	}

	b, _ := json.Marshal(cfg)
	if err := host.SaveConfig(ctx, orgID, pluginName, string(b)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?saved=1", http.StatusFound)
}

func handleAddInput(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg := configFromForm(r)
	cfg.Inputs = append(cfg.Inputs, PortDef{Name: "input" + strconv.Itoa(len(cfg.Inputs)+1), PortType: "number"})
	renderTempl(w, r, page(uiPageData{BasePath: basePath(r), Config: cfg}))
}

func handleDeleteInput(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	idx, _ := strconv.Atoi(r.PathValue("idx"))
	cfg := configFromForm(r)
	if idx >= 0 && idx < len(cfg.Inputs) {
		cfg.Inputs = append(cfg.Inputs[:idx], cfg.Inputs[idx+1:]...)
	}
	renderTempl(w, r, page(uiPageData{BasePath: basePath(r), Config: cfg}))
}

func handleAddOutput(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg := configFromForm(r)
	cfg.Outputs = append(cfg.Outputs, PortDef{Name: "output" + strconv.Itoa(len(cfg.Outputs)+1), PortType: "string"})
	renderTempl(w, r, page(uiPageData{BasePath: basePath(r), Config: cfg}))
}

func handleDeleteOutput(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	idx, _ := strconv.Atoi(r.PathValue("idx"))
	cfg := configFromForm(r)
	if idx >= 0 && idx < len(cfg.Outputs) {
		cfg.Outputs = append(cfg.Outputs[:idx], cfg.Outputs[idx+1:]...)
	}
	renderTempl(w, r, page(uiPageData{BasePath: basePath(r), Config: cfg}))
}

func basePath(r *http.Request) string {
	bp := r.Header.Get("X-Xolo-Plugin-Base-Path")
	if bp == "" {
		return "/"
	}
	return bp
}

func configFromForm(r *http.Request) Config {
	return Config{
		Script:  r.FormValue("script"),
		Inputs:  parsePortsFromForm(r, "input"),
		Outputs: parsePortsFromForm(r, "output"),
	}
}

func parsePortsFromForm(r *http.Request, prefix string) []PortDef {
	seen := map[int]bool{}
	nameKey := prefix + "_%d_name"
	_ = nameKey
	for key := range r.Form {
		p := prefix + "_"
		if strings.HasPrefix(key, p) && strings.HasSuffix(key, "_name") {
			middle := key[len(p) : len(key)-len("_name")]
			if n, err := strconv.Atoi(middle); err == nil {
				seen[n] = true
			}
		}
	}
	ports := make([]PortDef, 0, len(seen))
	for i := range len(seen) {
		ports = append(ports, PortDef{
			Name:     r.FormValue(prefix + "_" + strconv.Itoa(i) + "_name"),
			PortType: r.FormValue(prefix + "_" + strconv.Itoa(i) + "_type"),
		})
	}
	return ports
}

func renderTempl(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		slog.Error("script-processor/ui: render error", slog.Any("error", err))
	}
}
