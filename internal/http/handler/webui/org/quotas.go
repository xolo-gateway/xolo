package org

import (
	"context"
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
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
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

	existing, err := h.quotaStore.GetQuota(ctx, model.QuotaScopeOrg, string(org.ID()))
	loadError := ""
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		slog.ErrorContext(ctx, "could not load org budget", slogx.Error(err))
		loadError = "Budget indisponible : le store a renvoyé une erreur. Le formulaire ci-dessous reste éditable mais le total dépensé peut être inexact."
	}

	orgCurrency := org.Currency()
	if orgCurrency == "" {
		orgCurrency = model.DefaultCurrency
	}
	now := time.Now()
	dailyCost, dailyErr := h.loadOrgSpend(ctx, nil, org.ID(), startOfPeriod("day", now), orgCurrency)
	monthlyCost, monthlyErr := h.loadOrgSpend(ctx, nil, org.ID(), startOfPeriod("month", now), orgCurrency)
	yearlyCost, yearlyErr := h.loadOrgSpend(ctx, nil, org.ID(), startOfPeriod("year", now), orgCurrency)
	spendLoadError := ""
	for _, e := range []error{dailyErr, monthlyErr, yearlyErr} {
		if e != nil {
			slog.ErrorContext(ctx, "could not load org spend", slogx.Error(e))
			spendLoadError = "Consommation indisponible : le store a renvoyé une erreur. Les barres peuvent afficher 0 %."
			break
		}
	}
	switch {
	case loadError != "" && spendLoadError != "":
		loadError = loadError + " " + spendLoadError
	case spendLoadError != "":
		loadError = spendLoadError
	}

	vmodel := component.QuotaPageVModel{
		Org:         org,
		ScopeType:   "org",
		ScopeID:     string(org.ID()),
		Quota:       existing,
		Success:     r.URL.Query().Get("success"),
		DailyCost:   dailyCost,
		MonthlyCost: monthlyCost,
		YearlyCost:  yearlyCost,
		LoadError:   loadError,
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
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
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

	org, membership, err := h.resolveOrgAndMembership(ctx, orgSlug, membershipID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	existing, err := h.quotaStore.GetQuota(ctx, model.QuotaScopeUser, string(membership.UserID()))
	loadError := ""
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		slog.ErrorContext(ctx, "could not load member budget", slogx.Error(err))
		loadError = "Budget indisponible : le store a renvoyé une erreur. Le formulaire ci-dessous reste éditable mais le total dépensé peut être inexact."
	}

	orgCurrency := org.Currency()
	if orgCurrency == "" {
		orgCurrency = model.DefaultCurrency
	}
	now := time.Now()
	userIDs := []model.UserID{membership.UserID()}
	dailyCost, dailyErr := h.loadOrgSpend(ctx, userIDs, org.ID(), startOfPeriod("day", now), orgCurrency)
	monthlyCost, monthlyErr := h.loadOrgSpend(ctx, userIDs, org.ID(), startOfPeriod("month", now), orgCurrency)
	yearlyCost, yearlyErr := h.loadOrgSpend(ctx, userIDs, org.ID(), startOfPeriod("year", now), orgCurrency)
	spendLoadError := ""
	for _, e := range []error{dailyErr, monthlyErr, yearlyErr} {
		if e != nil {
			slog.ErrorContext(ctx, "could not load member spend", slogx.Error(e))
			spendLoadError = "Consommation indisponible : le store a renvoyé une erreur. Les barres peuvent afficher 0 %."
			break
		}
	}
	switch {
	case loadError != "" && spendLoadError != "":
		loadError = loadError + " " + spendLoadError
	case spendLoadError != "":
		loadError = spendLoadError
	}

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
		LoadError:   loadError,
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

	org, membership, err := h.resolveOrgAndMembership(ctx, orgSlug, membershipID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
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
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not save member quota", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/members/"+membershipID+"/quota?success=saved", http.StatusSeeOther)
}

// getApplicationQuotaPage renders the budget editor for one application. The
// view model is the same component as the org and member quota pages, just
// scoped to QuotaScopeApplication (issue #64): an application token can spend
// unattended, so the operator needs a way to cap its daily/monthly/yearly
// spend through the product rather than via a direct DB write or the
// provisioning API.
func (h *Handler) getApplicationQuotaPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	appID := r.PathValue("appID")

	org, app, err := h.resolveOrgAndApplication(ctx, orgSlug, appID)
	if err != nil {
		writeApplicationLookupError(ctx, w, err)
		return
	}

	existing, err := h.quotaStore.GetQuota(ctx, model.QuotaScopeApplication, appID)
	loadError := ""
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		slog.ErrorContext(ctx, "could not load application budget", slogx.Error(err))
		loadError = "Budget indisponible : le store a renvoyé une erreur. Le formulaire ci-dessous reste éditable mais le total dépensé peut être inexact."
	}

	orgCurrency := org.Currency()
	if orgCurrency == "" {
		orgCurrency = model.DefaultCurrency
	}
	now := time.Now()
	dailyCost, dailyErr := applicationSpend(ctx, h.usageStore, model.ApplicationID(appID), org.ID(), model.StartOfDay(now))
	monthlyCost, monthlyErr := applicationSpend(ctx, h.usageStore, model.ApplicationID(appID), org.ID(), model.StartOfMonth(now))
	yearlyCost, yearlyErr := applicationSpend(ctx, h.usageStore, model.ApplicationID(appID), org.ID(), model.StartOfYear(now))

	// A failing spend lookup collapses the consumed figure to zero. Surface it
	// on the page so the operator does not read a misleading 0% that happens
	// to be coincidentally close to the budget. Combine with the quota-load
	// error if both are present, so the operator sees one banner that covers
	// the whole page rather than two stacked ones.
	spendLoadError := ""
	for _, e := range []error{dailyErr, monthlyErr, yearlyErr} {
		if e != nil {
			slog.ErrorContext(ctx, "could not load application spend", slogx.Error(e))
			spendLoadError = "Consommation indisponible : le store a renvoyé une erreur. Les barres peuvent afficher 0 %."
			break
		}
	}
	switch {
	case loadError != "" && spendLoadError != "":
		loadError = loadError + " " + spendLoadError
	case spendLoadError != "":
		loadError = spendLoadError
	}

	vmodel := component.QuotaPageVModel{
		Org:         org,
		Application: app,
		ScopeType:   "application",
		ScopeID:     string(appID),
		Quota:       existing,
		Success:     r.URL.Query().Get("success"),
		DailyCost:   dailyCost,
		MonthlyCost: monthlyCost,
		YearlyCost:  yearlyCost,
		LoadError:   loadError,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + orgSlug + "-applications",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Applications", Href: "/orgs/" + orgSlug + "/admin/applications"},
				{Label: app.Name(), Href: "/orgs/" + orgSlug + "/admin/applications/" + string(appID) + "/edit"},
				{Label: "Budget", Href: ""},
			},
		},
	}

	templ.Handler(component.QuotaPage(vmodel)).ServeHTTP(w, r)
}

// saveApplicationQuota writes a QuotaScopeApplication row for the resolved
// application. The application is loaded again on POST so an operator cannot
// edit a budget on an application outside their organization by hand-crafting
// the form action.
func (h *Handler) saveApplicationQuota(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	appID := r.PathValue("appID")

	org, _, err := h.resolveOrgAndApplication(ctx, orgSlug, appID)
	if err != nil {
		writeApplicationLookupError(ctx, w, err)
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

	quota := model.NewQuota(model.QuotaScopeApplication, appID, currency, daily, monthly, yearly)
	if err := h.quotaStore.SetQuota(ctx, quota); err != nil {
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not save application quota", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/applications/"+appID+"/quota?success=saved", http.StatusSeeOther)
}

// applicationSpend returns the PAYG spending attributed to one application
// since the given time, in microcents of the org's base currency. It reuses
// the same counter the enforcer reads against (QuotaScopeApplication), so the
// figure shown to the operator matches the budget check exactly: a 429 from
// the enforcer and a 100% reading on the page both describe the same total.
//
// Costs are stored already converted to the org's currency at record time, so
// the counter is a flat microcent total: no per-currency grouping is needed
// here. This is the application's slice of org spending; cross-application
// rolls up at the org-wide scope.
//
// The request context is threaded through so a cancellation propagates to the
// store call; dropping it (context.Background) would let a cancelled client
// still pay the cost of the counter read. A non-nil error is logged and
// returned to the caller so it can surface a "Budget indisponible" banner
// alongside the zero figure — a silent 0% would otherwise hide a real store
// failure.
func applicationSpend(ctx context.Context, store port.UsageStore, appID model.ApplicationID, orgID model.OrgID, since time.Time) (int64, error) {
	total, err := store.SumQuotaCostSince(ctx, model.QuotaScopeApplication, string(appID), orgID, since)
	if err != nil {
		return 0, err
	}
	return total, nil
}

// parseBudgetField parses a currency budget field into microcents.
//
//   - Empty → nil (unlimited, the historical default).
//   - "0" → pointer to 0 (strict zero cap: any spend exceeds the budget and
//     the enforcer rejects every request — distinct from "unlimited").
//   - NaN, ±Inf, parse error → nil (caller cannot trust the input; treating
//     it as "unlimited" preserves the previous behaviour for now, but a future
//     iteration should return a validation error to surface the bad input).
//
// Tracking: issue #88 is the proper fix (return 400 + form re-render on
// invalid input). This function still swallows NaN/Inf for now.
func parseBudgetField(v string) *int64 {
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return nil
	}
	mc := int64(f * 1_000_000)
	return &mc
}
