package org

import (
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
)

func (h *Handler) getSettingsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	var eventsOverride *int
	if h.eventSettingsStore != nil {
		if override, err := h.eventSettingsStore.GetMaxEvents(ctx, org.ID()); err == nil {
			eventsOverride = override
		}
	}

	vmodel := component.OrgSettingsPageVModel{
		Org:               org,
		Success:           r.URL.Query().Get("success"),
		EventsMaxOverride: eventsOverride,
		EventsDefault:     h.eventsDefaultPerOrg,
		EventsGlobalCap:   h.eventsMaxPerOrg,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-settings",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Paramètres", Href: ""},
			},
		},
	}

	if org.ShareQuotaEqually() {
		orgQuota, err := h.quotaStore.GetQuota(ctx, model.QuotaScopeOrg, string(org.ID()))
		if err == nil && orgQuota != nil {
			members, _, err := h.orgStore.ListOrgMembers(ctx, org.ID(), port.ListOrgMembersOptions{})
			if err == nil && len(members) > 0 {
				n := int64(len(members))
				vmodel.MemberCount = len(members)
				if v := orgQuota.DailyBudget(); v != nil {
					divided := *v / n
					vmodel.SharedDailyBudget = &divided
				}
				if v := orgQuota.MonthlyBudget(); v != nil {
					divided := *v / n
					vmodel.SharedMonthlyBudget = &divided
				}
				if v := orgQuota.YearlyBudget(); v != nil {
					divided := *v / n
					vmodel.SharedYearlyBudget = &divided
				}
			}
		}
	}

	templ.Handler(component.OrgSettingsPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) saveSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	currency := r.FormValue("currency")
	if currency == "" {
		currency = model.DefaultCurrency
	}

	shareQuotaEqually := r.FormValue("share_quota_equally") == "on"

	updated := model.UpdateOrganization(org,
		model.WithOrgCurrency(currency),
		model.WithOrgShareQuotaEqually(shareQuotaEqually),
	)
	if err := h.orgStore.SaveOrg(ctx, updated); err != nil {
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not save org settings", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Events retention override: empty clears the override (uses the default).
	if h.eventSettingsStore != nil {
		var override *int
		if raw := r.FormValue("events_max"); raw != "" {
			if n, convErr := strconv.Atoi(raw); convErr == nil && n > 0 {
				if h.eventsMaxPerOrg > 0 && n > h.eventsMaxPerOrg {
					n = h.eventsMaxPerOrg
				}
				override = &n
			}
		}
		if err := h.eventSettingsStore.SetMaxEvents(ctx, org.ID(), override); err != nil {
			slog.ErrorContext(ctx, "could not save events retention", slogx.Error(err))
		}
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/settings?success=saved", http.StatusSeeOther)
}
