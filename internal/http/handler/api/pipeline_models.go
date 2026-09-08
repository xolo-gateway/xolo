package api

import (
	"net/http"
	"sort"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// pipelineModelOption is one entry of the model picker in the pipeline editor.
type pipelineModelOption struct {
	// ProxyName is the value a model_name port expects.
	ProxyName string `json:"proxyName"`
	// Kind is "model", "virtual" or "personal".
	Kind string `json:"kind"`
	// Description helps the user tell models apart; may be empty.
	Description string `json:"description,omitempty"`
	// Provider or owner, for grouping.
	Group string `json:"group,omitempty"`
}

// handlePipelineModels lists what a model_name port of the org's pipelines can
// name: the org's enabled models and its virtual models.
func (h *Handler) handlePipelineModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	org, err := h.orgStore.GetOrgBySlug(ctx, httpCtx.TenantID(ctx), r.PathValue("orgSlug"))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			writeError(w, http.StatusNotFound, "org not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if allowed, err := h.hasPermission(ctx, org.ID(), rbac.PermVirtualModelsRead); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	} else if !allowed {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	options, err := h.orgModelOptions(r, org, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, options)
}

// handlePersonalPipelineModels lists what a personal pipeline can name: the
// user's own virtual models (~/name) and, qualified by org slug, the models of
// every organisation the user belongs to.
func (h *Handler) handlePersonalPipelineModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	options := []pipelineModelOption{}

	if h.personalVMStore != nil {
		pvms, err := h.personalVMStore.ListPersonalVirtualModels(ctx, user.ID())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for _, vm := range pvms {
			options = append(options, pipelineModelOption{
				ProxyName:   "~/" + vm.Name(),
				Kind:        "personal",
				Description: vm.Description(),
				Group:       "Mes modèles",
			})
		}
	}

	memberships, err := h.orgStore.GetUserMemberships(ctx, user.ID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	for _, m := range memberships {
		org, err := h.orgStore.GetOrgByID(ctx, m.OrgID())
		if err != nil {
			continue
		}
		orgOptions, err := h.orgModelOptions(r, org, org.Slug()+"/")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		options = append(options, orgOptions...)
	}

	writeJSON(w, http.StatusOK, options)
}

// orgModelOptions returns the org's models and virtual models, proxy names
// prefixed as given (empty inside the org, "slug/" from a personal pipeline).
func (h *Handler) orgModelOptions(r *http.Request, org model.Organization, prefix string) ([]pipelineModelOption, error) {
	ctx := r.Context()
	options := []pipelineModelOption{}

	models, err := h.providerStore.ListEnabledLLMModels(ctx, org.ID())
	if err != nil {
		return nil, err
	}
	providerNames := map[model.ProviderID]string{}
	for _, m := range models {
		group, ok := providerNames[m.ProviderID()]
		if !ok {
			group = org.Name()
			if p, err := h.providerStore.GetProviderByID(ctx, m.ProviderID()); err == nil {
				group = p.Name()
			}
			providerNames[m.ProviderID()] = group
		}
		options = append(options, pipelineModelOption{
			ProxyName:   prefix + m.ProxyName(),
			Kind:        "model",
			Description: m.RealModel(),
			Group:       group,
		})
	}

	vms, err := h.virtualModelStore.ListVirtualModels(ctx, org.ID())
	if err != nil {
		return nil, err
	}
	for _, vm := range vms {
		options = append(options, pipelineModelOption{
			ProxyName:   prefix + vm.Name(),
			Kind:        "virtual",
			Description: vm.Description(),
			Group:       org.Name() + " · modèles virtuels",
		})
	}

	sort.SliceStable(options, func(i, j int) bool {
		if options[i].Group != options[j].Group {
			return options[i].Group < options[j].Group
		}
		return options[i].ProxyName < options[j].ProxyName
	})
	return options, nil
}
