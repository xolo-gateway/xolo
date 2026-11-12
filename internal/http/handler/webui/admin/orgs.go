package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/admin/component"
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

func (h *Handler) getOrgsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)

	// The platform console is tenant-scoped: a platform admin administers its
	// own tenant, never the whole instance.
	tenantID := httpCtx.TenantID(ctx)

	orgs, _, err := h.orgStore.ListOrgs(ctx, port.ListOrgsOptions{TenantID: &tenantID})
	if err != nil {
		slog.ErrorContext(ctx, "could not list orgs", slogx.Error(err))
		// La coquille est hx-boostée et ne remplace que #content : une réponse
		// en texte brut viderait la page.
		webcommon.HandleError(w, r, errors.WithStack(err))
		return
	}

	currency := model.DefaultCurrency
	since := time.Now().Add(-overviewWindow)

	// Usage is resolved for every organisation, then the rows are filtered: the
	// subtitle reports on the platform as a whole, not on the current filter.
	rows := h.overviewOrgs(ctx, orgs, since, currency)

	vmodel := component.OrgsPageVModel{
		Currency:  currency,
		TotalOrgs: len(rows),
		Search:    strings.TrimSpace(r.URL.Query().Get("q")),
		Status:    orgStatusFilter(r.URL.Query().Get("status")),
		Success:   r.URL.Query().Get("success"),
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "orgs",
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: i18n.T(ctx, "admin.breadcrumb.platform"), Href: "/admin/"},
				{Label: i18n.T(ctx, "admin.breadcrumb.orgs"), Href: "/admin/orgs"},
			},
			Context: common.ContextPlatform,
		},
	}

	for _, row := range rows {
		vmodel.TotalMembers += row.Members
	}

	vmodel.Orgs = filterOrgRows(rows, vmodel.Search, vmodel.Status)

	templ.Handler(component.OrgsPage(vmodel)).ServeHTTP(w, r)
}

// orgStatusFilter keeps only the segment values the list knows how to apply.
func orgStatusFilter(status string) string {
	switch status {
	case component.OrgStatusActive, component.OrgStatusInactive:
		return status
	default:
		return ""
	}
}

// filterOrgRows applies the search term and the status segment.
//
// The filtering happens in memory rather than in SQL: the console lists every
// tenant of the platform, a set counted in tens, and the rows already had to be
// fully materialised to carry their usage.
func filterOrgRows(rows []component.OverviewOrg, search, status string) []component.OverviewOrg {
	search = strings.ToLower(search)

	filtered := make([]component.OverviewOrg, 0, len(rows))
	for _, row := range rows {
		if status == component.OrgStatusActive && !row.Active {
			continue
		}
		if status == component.OrgStatusInactive && row.Active {
			continue
		}
		if search != "" &&
			!strings.Contains(strings.ToLower(row.Name), search) &&
			!strings.Contains(strings.ToLower(row.Slug), search) {
			continue
		}
		filtered = append(filtered, row)
	}

	return filtered
}

func (h *Handler) getNewOrgPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)

	vmodel := component.OrgFormVModel{
		IsNew: true,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "orgs",
			Breadcrumbs:  orgFormBreadcrumbs(ctx, nil),
			Context:      common.ContextPlatform,
		},
	}

	templ.Handler(component.OrgForm(vmodel)).ServeHTTP(w, r)
}

// createOrg creates the organisation of the submitted form, re-rendered with the
// reason when the submit is refused: the shell is hx-boosted and selects
// #content, which a bare http.Error does not carry.
func (h *Handler) createOrg(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		h.renderOrgFormError(w, r, nil, i18n.T(ctx, "admin.orgs.form.invalid_form"))
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	slug := strings.TrimSpace(r.FormValue("slug"))
	description := strings.TrimSpace(r.FormValue("description"))

	switch {
	case name == "":
		h.renderOrgFormError(w, r, nil, i18n.T(ctx, "admin.orgs.form.name_required"))
		return
	case slug == "":
		h.renderOrgFormError(w, r, nil, i18n.T(ctx, "admin.orgs.form.slug_required"))
		return
	case !model.IsValidSlug(slug):
		h.renderOrgFormError(w, r, nil, i18n.T(ctx, "admin.orgs.form.slug_invalid", model.MaxSlugLength))
		return
	}

	org := model.NewOrganization(httpCtx.TenantID(ctx), slug, name, description)
	if err := h.orgStore.CreateOrg(ctx, org); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrAlreadyExists) {
			h.renderOrgFormError(w, r, nil, i18n.T(ctx, "admin.orgs.form.slug_taken", slug))
			return
		}
		slog.ErrorContext(ctx, "could not create org", slogx.Error(err))
		webcommon.HandleError(w, r, errors.WithStack(err))
		return
	}

	// Provision the builtin roles (owner/admin/member) for the new org.
	if err := h.roleStore.EnsureBuiltinRoles(ctx, org.ID()); err != nil {
		// L'organisation est déjà enregistrée : sans message distinct, l'opérateur
		// recharge, resoumet, et tombe sur « identifiant déjà utilisé » au lieu du
		// vrai problème (rôles non provisionnés).
		slog.ErrorContext(ctx, "could not provision builtin roles", slogx.Error(err))
		webcommon.HandleError(w, r, webcommon.NewError(
			err.Error(),
			i18n.T(ctx, "admin.orgs.form.roles_failed"),
			http.StatusInternalServerError,
			common.LinkItem{
				URL:   common.BaseURLString(ctx, common.WithPath("/admin/orgs")),
				Label: i18n.T(ctx, "admin.orgs.title"),
			},
		))
		return
	}

	http.Redirect(w, r, "/admin/orgs?success=created", http.StatusSeeOther)
}

// renderOrgFormError re-renders the organisation form with errMsg and the
// submitted values, at 422. Creation and edition share it: the shell is
// hx-boosted and only swaps #content, which a bare http.Error does not carry.
//
// org is nil for the creation form; the edition form keeps the organisation it
// edits, so the action and the frozen slug stay right.
func (h *Handler) renderOrgFormError(w http.ResponseWriter, r *http.Request, org model.Organization, errMsg string) {
	h.renderOrgForm(w, r, org, errMsg, http.StatusUnprocessableEntity)
}

// renderOrgFormFailure renders the same form for a store failure at 500: an
// outage is not a refused submission, and monitoring must not read it as one.
func (h *Handler) renderOrgFormFailure(w http.ResponseWriter, r *http.Request, org model.Organization, errMsg string) {
	h.renderOrgForm(w, r, org, errMsg, http.StatusInternalServerError)
}

// renderOrgForm re-renders the organisation form with errMsg, the submitted
// values, and the given status.
func (h *Handler) renderOrgForm(w http.ResponseWriter, r *http.Request, org model.Organization, errMsg string, status int) {
	ctx := r.Context()

	vmodel := component.OrgFormVModel{
		Org:       org,
		IsNew:     org == nil,
		Error:     errMsg,
		Submitted: r.PostForm,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         httpCtx.User(ctx),
			SelectedItem: "orgs",
			Breadcrumbs:  orgFormBreadcrumbs(ctx, org),
			Context:      common.ContextPlatform,
		},
	}

	w.WriteHeader(status)
	templ.Handler(component.OrgForm(vmodel)).ServeHTTP(w, r)
}

// orgNotFound answers the organisation form asked for an organisation that does
// not exist, with the shared error page rather than a bare text body.
func orgNotFound(w http.ResponseWriter, r *http.Request) {
	webcommon.HandleError(w, r, webcommon.NewError(
		"organization not found",
		i18n.T(r.Context(), "admin.orgs.form.not_found"),
		http.StatusNotFound,
	))
}

// orgFormBreadcrumbs is the trail of the creation and edition forms: they end on
// the screen the form belongs to, "new" for the first, the organisation's name
// for the second.
func orgFormBreadcrumbs(ctx context.Context, org model.Organization) []common.BreadcrumbItem {
	crumbs := []common.BreadcrumbItem{
		{Label: i18n.T(ctx, "admin.breadcrumb.platform"), Href: "/admin/"},
		{Label: i18n.T(ctx, "admin.breadcrumb.orgs"), Href: "/admin/orgs"},
	}
	if org == nil {
		return append(crumbs, common.BreadcrumbItem{Label: i18n.T(ctx, "admin.breadcrumb.new_org"), Href: ""})
	}

	return append(crumbs, common.BreadcrumbItem{Label: org.Name(), Href: ""})
}

func (h *Handler) getEditOrgPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgID := r.PathValue("orgID")

	org, err := h.orgStore.GetOrgByID(ctx, model.OrgID(orgID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			orgNotFound(w, r)
			return
		}
		webcommon.HandleError(w, r, errors.WithStack(err))
		return
	}

	vmodel := component.OrgFormVModel{
		Org:   org,
		IsNew: false,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "orgs",
			Breadcrumbs:  orgFormBreadcrumbs(ctx, org),
			Context:      common.ContextPlatform,
		},
	}

	templ.Handler(component.OrgForm(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) updateOrg(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID := r.PathValue("orgID")

	org, err := h.orgStore.GetOrgByID(ctx, model.OrgID(orgID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			orgNotFound(w, r)
			return
		}
		webcommon.HandleError(w, r, errors.WithStack(err))
		return
	}

	if err := r.ParseForm(); err != nil {
		h.renderOrgFormError(w, r, org, i18n.T(ctx, "admin.orgs.form.invalid_form"))
		return
	}

	updated := model.UpdateOrganization(org,
		model.WithOrgName(r.FormValue("name")),
		model.WithOrgDescription(r.FormValue("description")),
		model.WithOrgActive(r.FormValue("active") == "on"),
	)

	if err := h.orgStore.SaveOrg(ctx, updated); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not save org", slogx.Error(err))
		// Le formulaire revient avec ce qui a été soumis : la coquille ne
		// remplace que #content, et une erreur en texte brut viderait la page.
		// 500 : un store en panne n'est pas un refus de validation.
		h.renderOrgFormFailure(w, r, org, i18n.T(ctx, "admin.orgs.form.save_failed"))
		return
	}

	http.Redirect(w, r, "/admin/orgs?success=saved", http.StatusSeeOther)
}
