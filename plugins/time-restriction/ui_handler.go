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
	mux.HandleFunc("POST /api/slots/add", handleAddSlot)
	mux.HandleFunc("POST /api/slots/delete", handleDeleteSlot)
	return uilocale.Handler(mux)
}

type uiPageData struct {
	BasePath string
	OrgID    string
	Config   Config
	Success  bool
	Error    string
}

func loadPageData(r *http.Request) (uiPageData, error) {
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
			slog.WarnContext(ctx, "time-restriction/ui: failed to load config", slog.Any("error", err))
		}
	}

	cfg, err := parseConfig(raw)
	if err != nil {
		cfg = Config{Timezone: "Europe/Paris"}
	}
	if cfg.Timezone == "" {
		cfg.Timezone = "Europe/Paris"
	}

	return uiPageData{
		BasePath: basePath,
		OrgID:    orgID,
		Config:   cfg,
	}, nil
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func handleIndex(w http.ResponseWriter, r *http.Request) {
	pd, err := loadPageData(r)
	if err != nil {
		httpErr(w, err)
		return
	}
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
		Timezone: strings.TrimSpace(r.FormValue("timezone")),
		Slots:    parseSlotsFromForm(r),
	}
	if cfg.Timezone == "" {
		cfg.Timezone = "Europe/Paris"
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

	http.Redirect(w, r, "/?saved=1", http.StatusFound)
}

// handleAddSlot re-renders the page with an extra empty slot appended.
func handleAddSlot(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg := Config{
		Timezone: r.FormValue("timezone"),
		Slots:    parseSlotsFromForm(r),
	}
	cfg.Slots = append(cfg.Slots, Slot{
		Days:  []string{"monday", "tuesday", "wednesday", "thursday", "friday"},
		Start: "09:00",
		End:   "18:00",
	})

	basePath := r.Header.Get("X-Xolo-Plugin-Base-Path")
	if basePath == "" {
		basePath = "/"
	}
	renderTempl(w, r, page(uiPageData{BasePath: basePath, Config: cfg}))
}

// handleDeleteSlot re-renders the page with the specified slot removed.
func handleDeleteSlot(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	idx, _ := strconv.Atoi(r.FormValue("index"))
	cfg := Config{
		Timezone: r.FormValue("timezone"),
		Slots:    parseSlotsFromForm(r),
	}
	if idx >= 0 && idx < len(cfg.Slots) {
		cfg.Slots = append(cfg.Slots[:idx], cfg.Slots[idx+1:]...)
	}

	basePath := r.Header.Get("X-Xolo-Plugin-Base-Path")
	if basePath == "" {
		basePath = "/"
	}
	renderTempl(w, r, page(uiPageData{BasePath: basePath, Config: cfg}))
}

// ── Form parsing ──────────────────────────────────────────────────────────────

// parseSlotsFromForm reads slot_N_start, slot_N_end and slot_N_days[] fields.
func parseSlotsFromForm(r *http.Request) []Slot {
	// Count distinct slot indices from slot_N_start keys.
	seen := map[int]bool{}
	for key := range r.Form {
		if strings.HasPrefix(key, "slot_") && strings.HasSuffix(key, "_start") {
			parts := strings.Split(key, "_")
			if len(parts) == 3 {
				if n, err := strconv.Atoi(parts[1]); err == nil {
					seen[n] = true
				}
			}
		}
	}

	slots := make([]Slot, 0, len(seen))
	for i := range len(seen) {
		slot := Slot{
			Start: r.FormValue("slot_" + strconv.Itoa(i) + "_start"),
			End:   r.FormValue("slot_" + strconv.Itoa(i) + "_end"),
			Days:  r.Form["slot_"+strconv.Itoa(i)+"_days"],
		}
		if slot.Start == "" {
			slot.Start = "09:00"
		}
		if slot.End == "" {
			slot.End = "18:00"
		}
		slots = append(slots, slot)
	}
	return slots
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func httpErr(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func renderTempl(w http.ResponseWriter, r *http.Request, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := component.Render(r.Context(), w); err != nil {
		slog.Error("time-restriction/ui: template render error", slog.Any("error", err))
	}
}
