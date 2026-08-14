package admin

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/admin/component"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/pkg/errors"
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
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
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
				{Label: "Plateforme", Href: "/admin/"},
				{Label: "Organisations", Href: "/admin/orgs"},
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
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: "Plateforme", Href: "/admin/"},
				{Label: "Organisations", Href: "/admin/orgs"},
				{Label: "Nouvelle organisation", Href: ""},
			},
			Context: common.ContextPlatform,
		},
	}

	templ.Handler(component.OrgForm(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) createOrg(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	slug := r.FormValue("slug")
	description := r.FormValue("description")

	if name == "" || slug == "" {
		http.Error(w, "Name and slug are required", http.StatusBadRequest)
		return
	}

	org := model.NewOrganization(httpCtx.TenantID(ctx), slug, name, description)
	if err := h.orgStore.CreateOrg(ctx, org); err != nil {
		slog.ErrorContext(ctx, "could not create org", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Provision the builtin roles (owner/admin/member) for the new org.
	if err := h.roleStore.EnsureBuiltinRoles(ctx, org.ID()); err != nil {
		slog.ErrorContext(ctx, "could not provision builtin roles", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/orgs?success=created", http.StatusSeeOther)
}

func (h *Handler) getEditOrgPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgID := r.PathValue("orgID")

	org, err := h.orgStore.GetOrgByID(ctx, model.OrgID(orgID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Organization not found", http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	vmodel := component.OrgFormVModel{
		Org:   org,
		IsNew: false,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "orgs",
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: "Plateforme", Href: "/admin/"},
				{Label: "Organisations", Href: "/admin/orgs"},
				{Label: org.Name(), Href: ""},
			},
			Context: common.ContextPlatform,
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
			http.Error(w, "Organization not found", http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	updated := model.UpdateOrganization(org,
		model.WithOrgName(r.FormValue("name")),
		model.WithOrgDescription(r.FormValue("description")),
		model.WithOrgActive(r.FormValue("active") == "on"),
	)

	if err := h.orgStore.SaveOrg(ctx, updated); err != nil {
		slog.ErrorContext(ctx, "could not save org", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/orgs?success=saved", http.StatusSeeOther)
}
