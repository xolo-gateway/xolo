package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/secretcleanup"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// ─── Request / response shapes ───────────────────────────────────────────────

// The graph GET/PUT logic and JSON response shape are shared with middlewares
// via pipeline_entity.go (pipelineResource, toEntityResponse).

type virtualModelRequest struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Graph       *model.PipelineGraph `json:"graph,omitempty"`
}

// vmResource adapts the virtual model store to the generic pipeline graph handlers.
func (h *Handler) vmResource() pipelineResource {
	return pipelineResource{
		get: func(ctx context.Context, id string) (model.PipelineEntity, error) {
			vm, err := h.virtualModelStore.GetVirtualModelByID(ctx, model.VirtualModelID(id))
			if err != nil {
				return nil, err
			}
			return vm.(model.PipelineEntity), nil
		},
		save: func(ctx context.Context, e model.PipelineEntity) error {
			return h.virtualModelStore.SaveVirtualModel(ctx, e.(model.VirtualModel))
		},
		readPerm:  rbac.PermVirtualModelsRead,
		writePerm: rbac.PermVirtualModelsWrite,
		notFound:  "virtual model not found",
	}
}

type nodeTypeDescriptor struct {
	Type       model.PipelineNodeType `json:"type"`
	PluginName string                 `json:"pluginName,omitempty"`
	// Version is the plugin's own version, shown in the editor palette. It is
	// empty for built-in node types, which are versioned with the server.
	Version      string                  `json:"version,omitempty"`
	Label        string                  `json:"label"`
	Description  string                  `json:"description"`
	InputPorts   []*proto.PortDescriptor `json:"inputPorts"`
	OutputPorts  []*proto.PortDescriptor `json:"outputPorts"`
	ConfigSchema string                  `json:"configSchema,omitempty"`
	HasUI        bool                    `json:"hasUI"`
}

// ─── List ─────────────────────────────────────────────────────────────────────

func (h *Handler) handleListVirtualModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "org not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if allowed, err := h.hasPermission(ctx, org.ID(), rbac.PermVirtualModelsRead); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	vms, err := h.virtualModelStore.ListVirtualModels(ctx, org.ID())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	out := make([]pipelineEntityResponse, 0, len(vms))
	for _, vm := range vms {
		out = append(out, toVMResponse(vm))
	}
	writeJSON(w, http.StatusOK, out)
}

// ─── Create ───────────────────────────────────────────────────────────────────

func (h *Handler) handleCreateVirtualModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "org not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if allowed, err := h.hasPermission(ctx, org.ID(), rbac.PermVirtualModelsWrite); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req virtualModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	vm := model.NewVirtualModel(org.ID(), req.Name, req.Description)
	if req.Graph != nil {
		vm.SetGraph(req.Graph)
	}

	if err := h.virtualModelStore.CreateVirtualModel(ctx, vm); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, toVMResponse(vm))
}

// ─── Get ──────────────────────────────────────────────────────────────────────

func (h *Handler) handleGetVirtualModel(w http.ResponseWriter, r *http.Request) {
	h.serveGetEntity(w, r, h.vmResource(), r.PathValue("vmID"))
}

// ─── Update ───────────────────────────────────────────────────────────────────

func (h *Handler) handleUpdateVirtualModel(w http.ResponseWriter, r *http.Request) {
	h.serveUpdateEntity(w, r, h.vmResource(), r.PathValue("vmID"))
}

// ─── Delete ───────────────────────────────────────────────────────────────────

func (h *Handler) handleDeleteVirtualModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vmID := model.VirtualModelID(r.PathValue("vmID"))

	vm, err := h.virtualModelStore.GetVirtualModelByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "virtual model not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if allowed, err := h.hasPermission(ctx, vm.OrgID(), rbac.PermVirtualModelsWrite); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !allowed {
		http.Error(w, "virtual model not found", http.StatusNotFound)
		return
	}

	if err := h.virtualModelStore.DeleteVirtualModel(ctx, vmID); err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "virtual model not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := secretcleanup.PruneRemovedNodes(ctx, h.secretStore, vm.Graph(), nil); err != nil {
		slog.ErrorContext(ctx, "could not prune secrets for deleted virtual model", slog.Any("error", err))
	}

	w.WriteHeader(http.StatusNoContent)
}

// ─── Pipeline node types ──────────────────────────────────────────────────────

func (h *Handler) handlePipelineNodeTypes(w http.ResponseWriter, r *http.Request) {
	var out []nodeTypeDescriptor

	// Built-in node types.
	out = append(out,
		nodeTypeDescriptor{
			Type:        model.NodeTypeGenerator,
			Label:       "Requête",
			Description: "Point d'entrée du pipeline — matérialise la requête LLM entrante.",
			InputPorts:  []*proto.PortDescriptor{},
			OutputPorts: []*proto.PortDescriptor{
				{Name: "request", PortType: string(model.PortTypeRequest), Required: true},
			},
		},
		nodeTypeDescriptor{
			Type:        model.NodeTypeSink,
			Label:       "Réponse",
			Description: "Point de sortie du pipeline — collecte la réponse LLM finale.",
			InputPorts: []*proto.PortDescriptor{
				{Name: "response", PortType: string(model.PortTypeResponse), Required: true},
			},
			OutputPorts: []*proto.PortDescriptor{},
		},
		nodeTypeDescriptor{
			Type:        model.NodeTypeValue,
			Label:       "Valeur",
			Description: "Émet une valeur statique (string, number ou boolean) sur son port de sortie.",
			InputPorts:  []*proto.PortDescriptor{},
			OutputPorts: []*proto.PortDescriptor{
				{Name: "value", PortType: string(model.PortTypeString)},
			},
			ConfigSchema: `{"type":"object","properties":{"portType":{"type":"string","enum":["string","number","boolean"]},"value":{"type":"string"}}}`,
		},
		nodeTypeDescriptor{
			Type:        model.NodeTypeModel,
			Label:       "Modèle LLM",
			Description: "Appelle un modèle LLM réel. model_name peut venir d'un port connecté ou d'une config statique.",
			InputPorts: []*proto.PortDescriptor{
				{Name: "request", PortType: string(model.PortTypeRequest), Required: true},
				{Name: "model_name", PortType: string(model.PortTypeString), Required: false},
			},
			OutputPorts: []*proto.PortDescriptor{
				{Name: "response", PortType: string(model.PortTypeResponse), Required: true},
			},
			ConfigSchema: `{"type":"object","properties":{"proxyName":{"type":"string","title":"Nom proxy (statique)"}}}`,
		},
	)

	// Plugin node types from loaded plugins.
	if h.pluginManager != nil {
		for _, desc := range h.pluginManager.List() {
			nd := nodeTypeDescriptor{
				Type:         model.NodeTypePlugin,
				PluginName:   desc.Name,
				Version:      desc.Version,
				Label:        desc.Name,
				Description:  desc.Description,
				InputPorts:   desc.InputPorts,
				OutputPorts:  desc.OutputPorts,
				ConfigSchema: desc.ConfigSchema,
				HasUI:        h.pluginManager.HTTPPort(desc.Name) > 0,
			}
			// Default ports if plugin didn't declare any.
			if nd.InputPorts == nil {
				nd.InputPorts = []*proto.PortDescriptor{}
			}
			if nd.OutputPorts == nil {
				nd.OutputPorts = []*proto.PortDescriptor{}
			}
			out = append(out, nd)
		}
	}

	writeJSON(w, http.StatusOK, out)
}

// ─── Export ───────────────────────────────────────────────────────────────────

func (h *Handler) handleExportVirtualModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vmID := model.VirtualModelID(r.PathValue("vmID"))

	vm, err := h.virtualModelStore.GetVirtualModelByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "virtual model not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if allowed, err := h.hasPermission(ctx, vm.OrgID(), rbac.PermVirtualModelsRead); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !allowed {
		http.Error(w, "virtual model not found", http.StatusNotFound)
		return
	}

	bundle := model.PipelineBundle{
		Version:     "1",
		ExportedAt:  time.Now().UTC(),
		Name:        vm.Name(),
		Description: vm.Description(),
		Graph:       vm.Graph(),
	}

	filename := fmt.Sprintf("pipeline-%s.json", sanitizeFilename(vm.Name()))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(bundle)
}

// ─── Import ───────────────────────────────────────────────────────────────────

func (h *Handler) handleImportVirtualModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), orgSlug)
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "org not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if allowed, err := h.hasPermission(ctx, org.ID(), rbac.PermVirtualModelsWrite); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var bundle model.PipelineBundle
	if err := json.NewDecoder(r.Body).Decode(&bundle); err != nil {
		http.Error(w, "invalid bundle JSON", http.StatusBadRequest)
		return
	}
	if bundle.Name == "" {
		http.Error(w, "bundle must contain a non-empty name", http.StatusBadRequest)
		return
	}

	var vm *model.BaseVirtualModel
	baseName := bundle.Name
	for attempt := 0; attempt <= 10; attempt++ {
		name := baseName
		if attempt > 0 {
			name = fmt.Sprintf("%s (%d)", baseName, attempt)
		}
		vm = model.NewVirtualModel(org.ID(), name, bundle.Description)
		if bundle.Graph != nil {
			vm.SetGraph(bundle.Graph)
		}
		err := h.virtualModelStore.CreateVirtualModel(ctx, vm)
		if err == nil {
			break
		}
		if errors.Is(err, port.ErrAlreadyExists) {
			if attempt == 10 {
				http.Error(w, "un modèle virtuel avec ce nom existe déjà (10 tentatives épuisées)", http.StatusConflict)
				return
			}
			continue
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, toVMResponse(vm))
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func sanitizeFilename(name string) string {
	replacer := strings.NewReplacer(
		"/", "-", "\\", "-", ":", "-", "*", "-",
		"?", "-", "\"", "-", "<", "-", ">", "-", "|", "-",
	)
	return replacer.Replace(name)
}

func toVMResponse(vm model.VirtualModel) pipelineEntityResponse {
	return toEntityResponse(vm.(model.PipelineEntity))
}
