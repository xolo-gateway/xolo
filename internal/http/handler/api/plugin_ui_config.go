package api

import (
	"encoding/json"
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/plugin"
	"github.com/pkg/errors"
)

func httpCtxUser(r *http.Request) model.User {
	return httpCtx.User(r.Context())
}

// pluginManagerWithHostService is the extended interface exposing HostService.
type pluginManagerWithHostService interface {
	pluginManagerIface
	HostService() *plugin.XoloHostService
}

// handleSeedPluginUIConfig pre-populates the host service in-memory store with
// the current node config before the plugin UI iframe is opened.
// PUT /api/orgs/{orgSlug}/plugin-ui-config?plugin=<name>
func (h *Handler) handleSeedPluginUIConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	pluginName := r.URL.Query().Get("plugin")
	if pluginName == "" {
		http.Error(w, "missing plugin query param", http.StatusBadRequest)
		return
	}

	org, err := h.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "org not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	hs, ok := h.pluginManager.(pluginManagerWithHostService)
	if !ok {
		http.Error(w, "plugin manager does not expose host service", http.StatusNotImplemented)
		return
	}

	var body struct {
		ConfigJSON string `json:"configJson"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body.ConfigJSON = "{}"
	}
	if body.ConfigJSON == "" {
		body.ConfigJSON = "{}"
	}

	hs.HostService().SeedConfig(string(org.ID()), pluginName, body.ConfigJSON)
	w.WriteHeader(http.StatusNoContent)
}

// handleSeedPersonalPluginUIConfig is the personal-context variant of handleSeedPluginUIConfig.
// It uses the authenticated user's ID as the scope key instead of an org ID.
// PUT /api/personal-plugin-ui-config?plugin=<name>
func (h *Handler) handleSeedPersonalPluginUIConfig(w http.ResponseWriter, r *http.Request) {
	pluginName := r.URL.Query().Get("plugin")
	if pluginName == "" {
		http.Error(w, "missing plugin query param", http.StatusBadRequest)
		return
	}

	user := httpCtxUser(r)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	hs, ok := h.pluginManager.(pluginManagerWithHostService)
	if !ok {
		http.Error(w, "plugin manager does not expose host service", http.StatusNotImplemented)
		return
	}

	var body struct {
		ConfigJSON string `json:"configJson"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body.ConfigJSON = "{}"
	}
	if body.ConfigJSON == "" {
		body.ConfigJSON = "{}"
	}

	hs.HostService().SeedConfig("~:"+string(user.ID()), pluginName, body.ConfigJSON)
	w.WriteHeader(http.StatusNoContent)
}

// handleReadPersonalPluginUIConfig is the personal-context variant of handleReadPluginUIConfig.
// GET /api/personal-plugin-ui-config?plugin=<name>
func (h *Handler) handleReadPersonalPluginUIConfig(w http.ResponseWriter, r *http.Request) {
	pluginName := r.URL.Query().Get("plugin")
	if pluginName == "" {
		http.Error(w, "missing plugin query param", http.StatusBadRequest)
		return
	}

	user := httpCtxUser(r)
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	hs, ok := h.pluginManager.(pluginManagerWithHostService)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"configJson": "{}"})
		return
	}

	cfgJSON := hs.HostService().ReadConfig("~:"+string(user.ID()), pluginName)
	writeJSON(w, http.StatusOK, map[string]string{"configJson": cfgJSON})
}

// handleReadPluginUIConfig returns the config last saved by the plugin UI.
// GET /api/orgs/{orgSlug}/plugin-ui-config?plugin=<name>
func (h *Handler) handleReadPluginUIConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	pluginName := r.URL.Query().Get("plugin")
	if pluginName == "" {
		http.Error(w, "missing plugin query param", http.StatusBadRequest)
		return
	}

	org, err := h.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "org not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	hs, ok := h.pluginManager.(pluginManagerWithHostService)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"configJson": "{}"})
		return
	}

	cfgJSON := hs.HostService().ReadConfig(string(org.ID()), pluginName)
	writeJSON(w, http.StatusOK, map[string]string{"configJson": cfgJSON})
}
