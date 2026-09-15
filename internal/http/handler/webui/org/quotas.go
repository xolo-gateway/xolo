package org

import (
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
)

func (h *Handler) getOrgQuotaPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	quotaStore := h.quotaStore

	existing, _ := quotaStore.GetQuota(ctx, model.QuotaScopeOrg, string(org.ID()))

	orgCurrency := org.Currency()
	if orgCurrency == "" {
		orgCurrency = model.DefaultCurrency
	}
	now := time.Now()
	dailyCost := h.sumConvertedCost(ctx, nil, org.ID(), startOfPeriod("day", now), orgCurrency)
	monthlyCost := h.sumConvertedCost(ctx, nil, org.ID(), startOfPeriod("month", now), orgCurrency)
	yearlyCost := h.sumConvertedCost(ctx, nil, org.ID(), startOfPeriod("year", now), orgCurrency)

	vmodel := component.QuotaPageVModel{
		Org:         org,
		ScopeType:   "org",
		ScopeID:     string(org.ID()),
		Quota:       existing,
		Success:     r.URL.Query().Get("success"),
		DailyCost:   dailyCost,
		MonthlyCost: monthlyCost,
		YearlyCost:  yearlyCost,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-quota",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Budget", Href: "/orgs/" + orgSlug + "/admin/quota"},
			},
		},
	}

	templ.Handler(component.QuotaPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) saveOrgQuota(w http.ResponseWriter, r *http.Request) {
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

	currency := org.Currency()
	if currency == "" {
		currency = model.DefaultCurrency
	}
	daily := parseBudgetField(r.FormValue("daily_budget"))
	monthly := parseBudgetField(r.FormValue("monthly_budget"))
	yearly := parseBudgetField(r.FormValue("yearly_budget"))

	quotaStore := h.quotaStore

	quota := model.NewQuota(model.QuotaScopeOrg, string(org.ID()), currency, daily, monthly, yearly)
	if err := quotaStore.SetQuota(ctx, quota); err != nil {
		slog.ErrorContext(ctx, "could not save org quota", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/quota?success=saved", http.StatusSeeOther)
}

func (h *Handler) getMemberQuotaPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	membershipID := r.PathValue("membershipID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	membership, err := h.orgStore.GetMembership(ctx, model.MembershipID(membershipID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Membership not found", http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	quotaStore := h.quotaStore

	existing, _ := quotaStore.GetQuota(ctx, model.QuotaScopeUser, string(membership.UserID()))

	orgCurrency := org.Currency()
	if orgCurrency == "" {
		orgCurrency = model.DefaultCurrency
	}
	now := time.Now()
	userIDs := []model.UserID{membership.UserID()}
	dailyCost := h.sumConvertedCost(ctx, userIDs, org.ID(), startOfPeriod("day", now), orgCurrency)
	monthlyCost := h.sumConvertedCost(ctx, userIDs, org.ID(), startOfPeriod("month", now), orgCurrency)
	yearlyCost := h.sumConvertedCost(ctx, userIDs, org.ID(), startOfPeriod("year", now), orgCurrency)

	vmodel := component.QuotaPageVModel{
		Org:         org,
		Membership:  membership,
		ScopeType:   "user",
		ScopeID:     string(membership.UserID()),
		Quota:       existing,
		Success:     r.URL.Query().Get("success"),
		DailyCost:   dailyCost,
		MonthlyCost: monthlyCost,
		YearlyCost:  yearlyCost,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-members",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Membres", Href: "/orgs/" + orgSlug + "/admin/members"},
				{Label: membership.User().DisplayName(), Href: ""},
				{Label: "Budget", Href: ""},
			},
		},
	}

	templ.Handler(component.QuotaPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) saveMemberQuota(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	membershipID := r.PathValue("membershipID")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	membership, err := h.orgStore.GetMembership(ctx, model.MembershipID(membershipID))
	if err != nil {
		if errors.Is(err, port.ErrNotFound) {
			http.Error(w, "Membership not found", http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	currency := org.Currency()
	if currency == "" {
		currency = model.DefaultCurrency
	}
	daily := parseBudgetField(r.FormValue("daily_budget"))
	monthly := parseBudgetField(r.FormValue("monthly_budget"))
	yearly := parseBudgetField(r.FormValue("yearly_budget"))

	quotaStore := h.quotaStore

	quota := model.NewQuota(model.QuotaScopeUser, string(membership.UserID()), currency, daily, monthly, yearly)
	if err := quotaStore.SetQuota(ctx, quota); err != nil {
		slog.ErrorContext(ctx, "could not save member quota", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/members/"+membershipID+"/quota?success=saved", http.StatusSeeOther)
}

// parseBudgetField parses a currency budget field into microcents. Empty → nil (unlimited).
func parseBudgetField(v string) *int64 {
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	// NaN and ±Inf parse fine and slip past "f <= 0"; int64(NaN * 1e6) is then
	// an arbitrary amount, not an error.
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return nil
	}
	mc := int64(f * 1_000_000)
	return &mc
}
