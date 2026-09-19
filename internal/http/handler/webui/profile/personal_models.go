package profile

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/secretcleanup"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/profile/component"
)

func (h *Handler) getPersonalModelsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)

	vms, err := h.personalVMStore.ListPersonalVirtualModels(ctx, user.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list personal virtual models", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	baseURL := httpCtx.BaseURL(ctx)

	vmodel := component.PersonalModelsPageVModel{
		VirtualModels: vms,
		Selected:      selectedPersonalModel(vms, r.URL.Query().Get("vm")),
		BaseURL:       baseURL.String(),
		Success:       r.URL.Query().Get("success"),
		Error:         r.URL.Query().Get("error"),
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "personal-models",
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: "Espace personnel", Href: "/usage"},
				{Label: "Mes modèles", Href: ""},
			},
			Context: common.ContextPersonal,
		},
	}

	templ.Handler(component.PersonalModelsPage(vmodel)).ServeHTTP(w, r)
}

// selectedPersonalModel resolves the model the right pane previews: the one
// named by `?vm=`, or the first of the list so the pane is never empty when
// there is something to show.
func selectedPersonalModel(vms []model.PersonalVirtualModel, id string) model.PersonalVirtualModel {
	if len(vms) == 0 {
		return nil
	}

	for _, vm := range vms {
		if string(vm.ID()) == id {
			return vm
		}
	}

	return vms[0]
}

func (h *Handler) getNewPersonalModelPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)

	vmodel := component.PersonalModelFormVModel{
		IsNew: true,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "personal-models",
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: "Espace personnel", Href: "/usage"},
				{Label: "Mes modèles", Href: "/profile/personal-models"},
				{Label: "Nouveau", Href: ""},
			},
			Context: common.ContextPersonal,
		},
	}

	templ.Handler(component.PersonalModelFormPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) createPersonalModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")

	// A blank name is rejected here. The input is marked `required` in the
	// form, but the attribute is bypassable, and we want the same response
	// as the update path and the org-side create path: ?error=name_required,
	// mapped to "Le nom est obligatoire." in common/flash.templ.
	if name == "" {
		http.Redirect(w, r, "/profile/personal-models?error=name_required", http.StatusSeeOther)
		return
	}

	// Uniqueness pre-check. See createVirtualModel for the rationale.
	existing, err := h.personalVMStore.GetPersonalVirtualModelByName(ctx, user.ID(), name)
	switch {
	case err == nil:
		if existing != nil {
			http.Redirect(w, r, "/profile/personal-models?error=exists", http.StatusSeeOther)
			return
		}
	case errors.Is(err, port.ErrNotFound):
		// No collision, proceed.
	default:
		slog.ErrorContext(ctx, "could not check personal virtual model name uniqueness", slogx.Error(err))
		http.Redirect(w, r, "/profile/personal-models?error=create_failed", http.StatusSeeOther)
		return
	}

	vm := model.NewPersonalVirtualModel(user.ID(), name, description)
	if err := h.personalVMStore.CreatePersonalVirtualModel(ctx, vm); err != nil {
		slog.ErrorContext(ctx, "could not create personal virtual model", slogx.Error(err))
		http.Redirect(w, r, "/profile/personal-models?error=create_failed", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/profile/personal-models?success=created", http.StatusSeeOther)
}

func (h *Handler) getEditPersonalModelPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	vmID := r.PathValue("vmID")

	vm, err := h.personalVMStore.GetPersonalVirtualModelByID(ctx, model.PersonalVirtualModelID(vmID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.ErrorContext(ctx, "could not get personal virtual model", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if vm.UserID() != user.ID() {
		http.NotFound(w, r)
		return
	}

	vmodel := component.PersonalModelFormVModel{
		VirtualModel: vm,
		IsNew:        false,
		Name:         vm.Name(),
		Description:  vm.Description(),
		Error:        r.URL.Query().Get("error"),
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "personal-models",
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: "Espace personnel", Href: "/usage"},
				{Label: "Mes modèles", Href: "/profile/personal-models"},
				{Label: vm.Name(), Href: ""},
			},
			Context: common.ContextPersonal,
		},
	}

	templ.Handler(component.PersonalModelFormPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) updatePersonalModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	vmID := r.PathValue("vmID")

	vm, err := h.personalVMStore.GetPersonalVirtualModelByID(ctx, model.PersonalVirtualModelID(vmID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.ErrorContext(ctx, "could not get personal virtual model", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if vm.UserID() != user.ID() {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")

	// A blank name is rejected here. The input is marked `required` in the
	// form, but the attribute is bypassable. We redirect to
	// ?error=name_required, the same flash code used by createPersonalModel
	// and createVirtualModel, mapped to "Le nom est obligatoire." in
	// common/flash.templ.
	if name == "" {
		http.Redirect(w, r, "/profile/personal-models/"+vmID+"/edit?error=name_required", http.StatusSeeOther)
		return
	}

	if name != vm.Name() {
		existing, err := h.personalVMStore.GetPersonalVirtualModelByName(ctx, user.ID(), name)
		switch {
		case err == nil:
			// Mirror of updateVirtualModel: the outer `name != vm.Name()`
			// guard makes a collision with the current row impossible since
			// GetPersonalVirtualModelByName matches on (user_id, name).
			if existing != nil {
				http.Redirect(w, r, "/profile/personal-models/"+vmID+"/edit?error=exists", http.StatusSeeOther)
				return
			}
		case errors.Is(err, port.ErrNotFound):
			// No collision, proceed.
		default:
			// See updateVirtualModel — surface a store error other than
			// not-found instead of silently treating it as "no conflict".
			slog.ErrorContext(ctx, "could not check personal virtual model name uniqueness", slogx.Error(err))
			http.Redirect(w, r, "/profile/personal-models/"+vmID+"/edit?error=update_failed", http.StatusSeeOther)
			return
		}
	}

	type mutable interface {
		SetName(string)
		SetDescription(string)
		SetUpdatedAt(time.Time)
	}

	m, ok := vm.(mutable)
	if !ok {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	m.SetName(name)
	m.SetDescription(description)
	m.SetUpdatedAt(time.Now())

	if err := h.personalVMStore.SavePersonalVirtualModel(ctx, vm); err != nil {
		if errors.Is(err, port.ErrAlreadyExists) {
			// Mirror of updateVirtualModel — concurrent rename caught by the
			// idx_pvm_user_name unique index after the pre-check passed.
			http.Redirect(w, r, "/profile/personal-models/"+vmID+"/edit?error=exists", http.StatusSeeOther)
			return
		}
		slog.ErrorContext(ctx, "could not save personal virtual model", slogx.Error(err))
		http.Redirect(w, r, "/profile/personal-models/"+vmID+"/edit?error=update_failed", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/profile/personal-models?success=updated", http.StatusSeeOther)
}

func (h *Handler) deletePersonalModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	vmID := r.PathValue("vmID")

	vm, err := h.personalVMStore.GetPersonalVirtualModelByID(ctx, model.PersonalVirtualModelID(vmID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.ErrorContext(ctx, "could not get personal virtual model", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if vm.UserID() != user.ID() {
		http.NotFound(w, r)
		return
	}

	if err := h.personalVMStore.DeletePersonalVirtualModel(ctx, model.PersonalVirtualModelID(vmID)); err != nil {
		slog.ErrorContext(ctx, "could not delete personal virtual model", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if err := secretcleanup.PruneRemovedNodes(ctx, h.secretStore, vm.Graph(), nil); err != nil {
		slog.ErrorContext(ctx, "could not prune secrets for deleted personal virtual model", slogx.Error(err))
	}

	http.Redirect(w, r, "/profile/personal-models?success=deleted", http.StatusSeeOther)
}

func (h *Handler) getPersonalPipelineEditorPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	vmID := r.PathValue("vmID")

	vm, err := h.personalVMStore.GetPersonalVirtualModelByID(ctx, model.PersonalVirtualModelID(vmID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.ErrorContext(ctx, "could not get personal virtual model", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if vm.UserID() != user.ID() {
		http.NotFound(w, r)
		return
	}

	baseURL := httpCtx.BaseURL(ctx)

	vmodel := component.PersonalModelEditorVModel{
		VM:      vm,
		APIBase: baseURL.String(),
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "personal-models",
			FullBleed:    true,
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: "Espace personnel", Href: "/usage"},
				{Label: "Mes modèles", Href: "/profile/personal-models"},
				{Label: vm.Name(), Href: ""},
			},
			Context: common.ContextPersonal,
		},
	}

	templ.Handler(component.PersonalModelEditorPage(vmodel)).ServeHTTP(w, r)
}
