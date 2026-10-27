package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/bornholm/go-fuzzy/dsl"
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
	mux.HandleFunc("POST /api/validate", handleValidateDSL)
	return uilocale.Handler(mux)
}

type uiPageData struct {
	BasePath    string
	OrgID       string
	Config      Config
	Success     bool
	ValidateErr string
	ValidateOK  bool
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
			slog.WarnContext(ctx, "fuzzy-evaluator/ui: failed to load config", slog.Any("error", err))
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
		http.Error(w, "missing host or org context", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cfg := Config{
		RulesDSL: strings.TrimSpace(r.FormValue("rules_dsl")),
		Inputs:   parsePortsFromForm(r, "input"),
		Outputs:  parsePortsFromForm(r, "output"),
	}
	if cfg.RulesDSL == "" {
		cfg.RulesDSL = DefaultRulesDSL
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
	cfg.Inputs = append(cfg.Inputs, PortDef{Name: "var" + strconv.Itoa(len(cfg.Inputs)+1)})
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
	cfg.Outputs = append(cfg.Outputs, PortDef{Name: "out" + strconv.Itoa(len(cfg.Outputs)+1)})
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

func handleValidateDSL(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": err.Error()})
		return
	}
	dslText := r.FormValue("rules_dsl")
	w.Header().Set("Content-Type", "application/json")
	if _, err := dsl.ParseRulesAndVariables(dslText); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"valid": true})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func configFromForm(r *http.Request) Config {
	return Config{
		RulesDSL: r.FormValue("rules_dsl"),
		Inputs:   parsePortsFromForm(r, "input"),
		Outputs:  parsePortsFromForm(r, "output"),
	}
}

func parsePortsFromForm(r *http.Request, prefix string) []PortDef {
	seen := map[int]bool{}
	key := prefix + "_"
	suffix := "_name"
	for k := range r.Form {
		if strings.HasPrefix(k, key) && strings.HasSuffix(k, suffix) {
			mid := k[len(key) : len(k)-len(suffix)]
			if n, err := strconv.Atoi(mid); err == nil {
				seen[n] = true
			}
		}
	}
	ports := make([]PortDef, 0, len(seen))
	for i := range len(seen) {
		ports = append(ports, PortDef{Name: r.FormValue(key + strconv.Itoa(i) + suffix)})
	}
	return ports
}

func basePath(r *http.Request) string {
	bp := r.Header.Get("X-Xolo-Plugin-Base-Path")
	if bp == "" {
		return "/"
	}
	return bp
}

func renderTempl(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		slog.Error("fuzzy-evaluator/ui: render error", slog.Any("error", err))
	}
}
