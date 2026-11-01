package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/a-h/templ"
	goanon "github.com/bornholm/go-anon"
	"github.com/bornholm/go-anon/pkg/modelstore"
	"github.com/xolo-gateway/xolo/internal/i18n"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	"github.com/xolo-gateway/xolo/plugins/internal/uilocale"
)

type pluginUI struct {
	plugin *Plugin
}

func newUIHandler(p *Plugin) http.Handler {
	ui := &pluginUI{plugin: p}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", ui.handleIndex)
	mux.HandleFunc("POST /api/config", ui.handleSaveConfig)
	mux.HandleFunc("POST /api/secrets/hash_key", ui.handleSaveHashKey)
	mux.HandleFunc("POST /api/secrets/hash_key/delete", ui.handleDeleteHashKey)
	return uilocale.Handler(mux)
}

// uiPageData holds all data needed to render the configuration page.
type uiPageData struct {
	BasePath    string
	OrgID       string
	NodeID      string
	Config      Config
	Success     bool
	Error       string
	ModelStatus []modelStatusEntry
	// Languages lists the languages selectable in the form: those handled by
	// the go-anon pipeline for which a model is published.
	Languages []string
	// Indecis is the status of the indecis model, for the indecis engine.
	Indecis indecisStatus
	// HasHashKey indique si une clé HMAC est déjà stockée pour ce nœud.
	HasHashKey bool
}

// indecisStatus says which indecis model the configuration reads with and
// whether it still has to be downloaded.
type indecisStatus struct {
	Name   string
	Path   string
	Cached bool
	// Local is a directory given by the operator, used as is.
	Local bool
}

type modelStatusEntry struct {
	Lang   string
	Cached bool
}

func (ui *pluginUI) loadPageData(r *http.Request) (uiPageData, error) {
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
			slog.WarnContext(ctx, "pseudonymizer/ui: failed to load config", slog.Any("error", err))
		}
	}

	cfg, err := parseConfig(raw)
	if err != nil {
		cfg = defaultConfig()
	}

	pd := uiPageData{
		BasePath: basePath,
		OrgID:    orgID,
		Config:   cfg,
	}

	scopeID := r.Header.Get("X-Xolo-Secret-Scope-Id")
	pd.NodeID = r.Header.Get("X-Xolo-Node-Id")
	if host != nil && scopeID != "" && pd.NodeID != "" {
		_, found, err := host.GetSecret(ctx, scopeID, pluginName, pd.NodeID, secretKeyHashHMAC)
		if err != nil {
			slog.WarnContext(ctx, "pseudonymizer/ui: failed to check hash secret", slog.Any("error", err))
		} else {
			pd.HasHashKey = found
		}
	}

	// Both statuses: the page follows a change of engine before it is saved.
	pd.Indecis = indecisStatusOf(ctx, cfg)
	pd.ModelStatus, pd.Languages = ui.fetchModelStatus(ctx, cfg)
	if cfg.Engine == engineIndecis {
		// Every language reads with the one model, whatever the CRF models
		// published.
		pd.Languages = goanon.SupportedLanguages()
	}
	return pd, nil
}

// indecisStatusOf reads the indecis model status from the disk, without
// downloading.
func indecisStatusOf(ctx context.Context, cfg Config) indecisStatus {
	status := indecisStatus{Name: "indecis-ner " + modelName + " " + modelRelease, Local: cfg.ModelDir != ""}
	src, err := modelSourceFromConfig(cfg)
	if err != nil {
		slog.WarnContext(ctx, "pseudonymizer/ui: failed to resolve the model directory", slog.Any("error", err))
		return status
	}
	if status.Local {
		status.Name = filepath.Base(src.path())
	}
	status.Path = src.path()
	status.Cached = src.cached()
	return status
}

// fetchModelStatus builds a lightweight model status list (no downloads) and
// the list of languages usable for anonymization.
func (ui *pluginUI) fetchModelStatus(ctx context.Context, cfg Config) ([]modelStatusEntry, []string) {
	opts := storeOptionsFromConfig(cfg)
	store, err := modelstore.New(opts...)
	if err != nil {
		slog.WarnContext(ctx, "pseudonymizer/ui: failed to create model store", slog.Any("error", err))
		return nil, goanon.SupportedLanguages()
	}

	langs, err := store.Available(ctx)
	if err != nil {
		slog.WarnContext(ctx, "pseudonymizer/ui: failed to list available models", slog.Any("error", err))
		return nil, supportedLanguages(ctx, nil)
	}

	entries := make([]modelStatusEntry, 0, len(langs))
	for _, lang := range langs {
		entries = append(entries, modelStatusEntry{
			Lang:   lang,
			Cached: store.IsCached(lang),
		})
	}
	return entries, supportedLanguages(ctx, store)
}

func (ui *pluginUI) handleIndex(w http.ResponseWriter, r *http.Request) {
	pd, err := ui.loadPageData(r)
	if err != nil {
		httpError(w, err)
		return
	}
	pd.Success = r.URL.Query().Get("saved") == "1"
	renderTempl(w, r, page(pd))
}

func (ui *pluginUI) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
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

	// La configuration courante sert de base : le formulaire ne couvre pas tous
	// les réglages du schéma (skip_types, blocklist, hash_scope…), et repartir
	// d'une Config vide les effacerait à chaque sauvegarde.
	base := defaultConfig()
	if raw, err := host.GetConfig(ctx, orgID, pluginName); err != nil {
		slog.WarnContext(ctx, "pseudonymizer/ui: failed to load config before save", slog.Any("error", err))
	} else if parsed, err := parseConfig(raw); err == nil {
		base = parsed
	}

	cfg := configFromForm(r, base)

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

func (ui *pluginUI) handleSaveHashKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scopeID := r.Header.Get("X-Xolo-Secret-Scope-Id")
	nodeID := r.Header.Get("X-Xolo-Node-Id")
	host := pluginsdk.HostClientFromContext(ctx)
	pluginName := pluginsdk.PluginNameFromContext(ctx)

	if host == nil || scopeID == "" {
		http.Error(w, "missing host or org context", http.StatusBadRequest)
		return
	}
	if nodeID == "" {
		http.Error(w, "missing node context", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	value := r.FormValue("hash_key")
	if value == "" {
		http.Error(w, i18n.T(ctx, "plugin.pseudonymizer.hash_key.empty"), http.StatusBadRequest)
		return
	}
	if _, err := goanon.ParseHashKey(value); err != nil {
		http.Error(w, i18n.T(ctx, "plugin.pseudonymizer.hash_key.invalid", err.Error()), http.StatusBadRequest)
		return
	}
	if err := host.SetSecret(ctx, scopeID, pluginName, nodeID, secretKeyHashHMAC, value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?saved=1", http.StatusFound)
}

func (ui *pluginUI) handleDeleteHashKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scopeID := r.Header.Get("X-Xolo-Secret-Scope-Id")
	nodeID := r.Header.Get("X-Xolo-Node-Id")
	host := pluginsdk.HostClientFromContext(ctx)
	pluginName := pluginsdk.PluginNameFromContext(ctx)

	if host == nil || scopeID == "" || nodeID == "" {
		http.Error(w, "missing host, org or node context", http.StatusBadRequest)
		return
	}
	if err := host.DeleteSecret(ctx, scopeID, pluginName, nodeID, secretKeyHashHMAC); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?saved=1", http.StatusFound)
}

// configFromForm applies the form values on top of base, which carries the
// settings the form does not expose.
func configFromForm(r *http.Request, base Config) Config {
	cfg := base
	// The types the page showed, checked or not: without them, an old form
	// or an API call keeps the skipped types as they were.
	if shown := r.Form["types_shown"]; len(shown) > 0 {
		checked := r.Form["types"]
		cfg.SkipTypes = skipTypesOf(base.SkipTypes, shown, checked)
		// The regular expressions of a family run as long as one of its
		// types is checked: the page has no other switch for them.
		cfg.BuiltinRegexPatterns = groupChecked(groupIdentifiers, checked)
		cfg.BuiltinSecretPatterns = groupChecked(groupSecrets, checked)
	}
	// The leak choice sets the verification fields; without it, they are kept.
	switch r.FormValue("leak") {
	case "off":
		cfg.Verification, cfg.VerificationStrict = false, false
	case "report":
		cfg.Verification, cfg.VerificationStrict = true, false
	case "block":
		cfg.Verification, cfg.VerificationStrict, cfg.VerificationOnLeak = true, true, "block"
	}
	// An absent engine keeps the node's: a partial form must not turn an
	// indecis node back to the CRF.
	if engine := r.FormValue("engine"); engine != "" {
		cfg.Engine = engine
	}
	cfg.CacheDir = r.FormValue("cache_dir")
	cfg.ManifestURL = r.FormValue("manifest_url")
	cfg.ModelDir = r.FormValue("model_dir")
	cfg.ModelURL = r.FormValue("model_url")
	cfg.Offline = r.FormValue("offline") == "on"
	cfg.Language = r.FormValue("language")
	cfg.FallbackLanguage = r.FormValue("fallback_language")
	cfg.Strategy = r.FormValue("strategy")
	cfg.FirstNameReclassify = r.FormValue("first_name_reclassify") == "on"
	cfg.Merge = r.FormValue("merge") == "on"
	cfg.NameCompletion = r.FormValue("name_completion") == "on"
	cfg.SirenContextual = r.FormValue("siren_contextual") == "on"
	cfg.InjectInstruction = r.FormValue("inject_instruction") == "on"
	cfg.ProcessAttachments = r.FormValue("process_attachments") == "on"
	cfg.UnsupportedAttachments = r.FormValue("unsupported_attachments")

	// Un champ nombre laissé vide n'est pas transmis : le seuil doit alors
	// revenir à 0 plutôt que conserver la valeur de base.
	cfg.MinConfidence = 0
	cfg.MaxTokens = 0
	cfg.MinRunes = 0
	// Unlike the thresholds, an emptied or invalid bias goes back to the
	// default: a bias of 0 lets through what the measurements count as leaks.
	cfg.Bias = defaultBias
	if bias, err := strconv.ParseFloat(r.FormValue("bias"), 64); err == nil && bias >= 0 && bias <= maxBias {
		cfg.Bias = bias
	}
	if minConf := r.FormValue("min_confidence"); minConf != "" {
		var f float64
		if err := json.Unmarshal([]byte(minConf), &f); err == nil {
			cfg.MinConfidence = f
		}
	}
	if maxTok := r.FormValue("max_tokens"); maxTok != "" {
		var n int
		if err := json.Unmarshal([]byte(maxTok), &n); err == nil {
			cfg.MaxTokens = n
		}
	}
	if minRunes := r.FormValue("min_runes"); minRunes != "" {
		var n int
		if err := json.Unmarshal([]byte(minRunes), &n); err == nil {
			cfg.MinRunes = n
		}
	}

	cfg.MaxAttachmentBytes = formInt(r, "max_attachment_bytes", defaultMaxAttachmentBytes)
	cfg.MaxAttachmentChars = formInt(r, "max_attachment_chars", defaultMaxAttachmentChars)

	if cfg.UnsupportedAttachments != "block" && cfg.UnsupportedAttachments != "remove" {
		cfg.UnsupportedAttachments = "block"
	}

	if cfg.Language == "" {
		cfg.Language = LanguageAuto
	}
	if cfg.FallbackLanguage == "" {
		cfg.FallbackLanguage = defaultLanguage
	}
	if cfg.Strategy == "" {
		cfg.Strategy = "tag"
	}
	if cfg.Engine != engineIndecis {
		cfg.Engine = engineCRF
	}

	return cfg
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// groupChecked reports whether one type of the family group is among checked.
func groupChecked(group string, checked []string) bool {
	for _, g := range pageTypeGroups {
		if g.Key != group {
			continue
		}
		for _, t := range g.Types {
			if slices.Contains(checked, string(t.Type)) {
				return true
			}
		}
	}
	return false
}

// formInt reads an integer form field, falling back to fallback when the field
// is absent — an emptied limit means "back to the default", not "unlimited".
func formInt(r *http.Request, name string, fallback int) int {
	raw := r.FormValue(name)
	if raw == "" {
		return fallback
	}
	var n int
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		return fallback
	}
	return n
}

func httpError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func renderTempl(w http.ResponseWriter, r *http.Request, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := component.Render(r.Context(), w); err != nil {
		slog.Error("pseudonymizer/ui: template render error", slog.Any("error", err))
	}
}
