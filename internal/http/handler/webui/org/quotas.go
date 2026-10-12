package org

import (
	"context"
	"fmt"
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
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}
	h.renderOrgQuotaPage(w, r, org, "")
}

// renderOrgQuotaPage renders the budget of the organization, with formErr
// explaining why a submitted budget was refused.
func (h *Handler) renderOrgQuotaPage(w http.ResponseWriter, r *http.Request, org model.Organization, formErr string) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := org.Slug()

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
		Error:       formErr,
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
	user := httpCtx.User(ctx)
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
	// Load the existing quota so parseQuotaBudgetFields can preserve
	// fields the operator did not touch on a partial POST (issue #88).
	existing, err := h.quotaStore.GetQuota(ctx, model.QuotaScopeOrg, string(org.ID()))
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		slog.ErrorContext(ctx, "could not load org budget for save", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if existing == nil {
		existing = model.NewQuota(model.QuotaScopeOrg, string(org.ID()), currency, nil, nil, nil)
	}
	daily, monthly, yearly, fieldErrors := parseQuotaBudgetFields(r, existing)

	if len(fieldErrors) > 0 {
		// Re-render the form rather than 400 + redirect: an operator who
		// typed a bad value needs the page back with their input and the
		// reason it was rejected, not a generic error and certainly not a
		// ?success=saved banner that lies about the outcome (issue #88).
		h.renderOrgQuotaFormError(w, r, ctx, user, orgSlug, org, existing, fieldErrors)
		return
	}
	// Like a budget field, an absent cascade keeps the stored one.
	cascade := existing.Cascade().Normalize()
	if r.Form.Has("cascade") {
		cascade = model.QuotaCascade(r.FormValue("cascade")).Normalize()
	}
	if !cascade.Valid() {
		http.Error(w, "Invalid cascade", http.StatusBadRequest)
		return
	}

	quota := model.NewQuota(model.QuotaScopeOrg, string(org.ID()), currency, daily, monthly, yearly)
	quota.SetCascade(cascade)
	if !h.saveQuotaChecked(w, r, quota, func(msg string) { h.renderOrgQuotaPage(w, r, org, msg) }) {
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
	user := httpCtx.User(ctx)
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
	// Load the existing quota so parseQuotaBudgetFields can preserve
	// fields the operator did not touch on a partial POST (issue #88).
	existing, err := h.quotaStore.GetQuota(ctx, model.QuotaScopeUser, string(membership.UserID()))
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		slog.ErrorContext(ctx, "could not load member budget for save", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if existing == nil {
		existing = model.NewQuota(model.QuotaScopeUser, string(membership.UserID()), currency, nil, nil, nil)
	}
	daily, monthly, yearly, fieldErrors := parseQuotaBudgetFields(r, existing)

	if len(fieldErrors) > 0 {
		h.renderMemberQuotaFormError(w, r, ctx, user, orgSlug, org, membership, existing, fieldErrors)
		return
	}

	quota := model.NewQuota(model.QuotaScopeUser, string(membership.UserID()), currency, daily, monthly, yearly)
	if err := h.quotaStore.SetQuota(ctx, quota); err != nil {
		if webcommon.RejectLocked(w, r, err) {
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
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	appID := r.PathValue("appID")

	org, app, err := h.resolveOrgAndApplication(ctx, orgSlug, appID)
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
	// Load the existing quota so parseQuotaBudgetFields can preserve
	// fields the operator did not touch on a partial POST (issue #88).
	existing, err := h.quotaStore.GetQuota(ctx, model.QuotaScopeApplication, appID)
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		slog.ErrorContext(ctx, "could not load application budget for save", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if existing == nil {
		existing = model.NewQuota(model.QuotaScopeApplication, appID, currency, nil, nil, nil)
	}
	daily, monthly, yearly, fieldErrors := parseQuotaBudgetFields(r, existing)

	if len(fieldErrors) > 0 {
		h.renderApplicationQuotaFormError(w, r, ctx, user, orgSlug, org, app, existing, fieldErrors)
		return
	}

	quota := model.NewQuota(model.QuotaScopeApplication, appID, currency, daily, monthly, yearly)
	if err := h.quotaStore.SetQuota(ctx, quota); err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not save application quota", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/applications/"+appID+"/quota?success=saved", http.StatusSeeOther)
}

// parseQuotaBudgetFields runs parseBudgetField over the three budget
// inputs of the quota editor form. The first error wins per field, so a
// single field with two issues does not produce two messages; the whole
// map is still returned so the caller can highlight every offending
// input at once.
//
// One entry per invalid field: an absent key means the field parsed
// cleanly (a number was typed or the field was empty, both of which
// parseBudgetField accepts), and a present key carries the error string
// for the field's first issue. The map is nil when every field parsed
// cleanly — callers check len(fieldErrors) > 0 to decide whether to
// re-render the form.
//
// A field absent from r.Form keeps its value from the stored quota; a
// present-and-empty field stores nil (unlimited), which matches the
// "Un champ vide vaut « illimité »" help text. The distinction matters:
// the browser form always submits all three names, so an absent field
// only happens on a partial POST — a hand-crafted request, a re-submit
// after a rejection, or an API client omitting a key the operator never
// touched. The stored fallback is the right behaviour for the operator
// who edits one field at a time; nil is the right behaviour for the
// operator who explicitly cleared a field to remove the cap. The
// previous parser's r.FormValue conflates the two and returns nil for
// both, which is the silent data-loss class issue #88 is the fix for.
func parseQuotaBudgetFields(r *http.Request, stored model.Quota) (daily, monthly, yearly *int64, fieldErrors map[string]string) {
	// parse returns the parsed value (nil for absent or empty, both of
	// which parseBudgetField treats as "no cap"), a present flag, and
	// any error. The present flag lets the merge below fall back to
	// the stored value only when the operator did not touch the field;
	// a present-but-empty value stores nil (unlimited) as the help
	// text and the rest of the UI promise.
	parse := func(field string) (*int64, bool, error) {
		values, ok := r.Form[field]
		if !ok {
			return nil, false, nil // absent
		}
		v, err := parseBudgetField(values[0])
		return v, true, err
	}
	daily, dailyPresent, errD := parse("daily_budget")
	monthly, monthlyPresent, errM := parse("monthly_budget")
	yearly, yearlyPresent, errY := parse("yearly_budget")
	if !dailyPresent {
		daily = stored.DailyBudget()
	}
	if !monthlyPresent {
		monthly = stored.MonthlyBudget()
	}
	if !yearlyPresent {
		yearly = stored.YearlyBudget()
	}
	if errD != nil || errM != nil || errY != nil {
		fieldErrors = make(map[string]string)
		if errD != nil {
			fieldErrors["daily_budget"] = errD.Error()
		}
		if errM != nil {
			fieldErrors["monthly_budget"] = errM.Error()
		}
		if errY != nil {
			fieldErrors["yearly_budget"] = errY.Error()
		}
	}
	return daily, monthly, yearly, fieldErrors
}

// quotaFormSubmitted picks the raw string the operator typed out of the
// POSTed form, one entry per *present* budget field. The form re-renders
// with these values rather than the stored quota, so a rejected value
// does not silently vanish and the next save can be a fix-up of the same
// input rather than a re-typing from memory.
//
// Reading from r.Form (not r.FormValue) is what makes this distinction
// work: r.FormValue returns "" for both an absent key and a present-but-
// empty value, and treating them as the same erases the stored budget for
// every field the operator did not re-submit. With r.Form, an absent key
// means the operator did not touch that field and budgetInputValue falls
// back to the stored value; a present key with an empty string means the
// operator cleared the field intentionally and the re-render keeps it
// empty. Conflating the two was the silent-data-loss regression Conclave
// review #2 caught (issue #88).
func quotaFormSubmitted(r *http.Request) map[string]string {
	submitted := make(map[string]string, 3)
	for _, field := range []string{"daily_budget", "monthly_budget", "yearly_budget"} {
		if values, ok := r.Form[field]; ok && len(values) > 0 {
			submitted[field] = values[0]
		}
	}
	return submitted
}

// quotaSpendLoader abstracts the three ways the quota editor reads the
// spent-so-far totals for a scope. Org and member scopes both go through
// loadOrgSpend (with a nil vs per-member user list), application scope goes
// through applicationSpend (issue #64). Returning a single function type
// lets renderQuotaFormError share its body across the three editors.
type quotaSpendLoader func(ctx context.Context, currency string) (daily, monthly, yearly int64, err error)

// loadOrgScopeSpend is the org-wide loader: every user in the org counts.
func (h *Handler) loadOrgScopeSpend(ctx context.Context, org model.Organization, currency string) (int64, int64, int64, error) {
	now := time.Now()
	daily, err := h.loadOrgSpend(ctx, nil, org.ID(), startOfPeriod("day", now), currency)
	if err != nil {
		return 0, 0, 0, err
	}
	monthly, err := h.loadOrgSpend(ctx, nil, org.ID(), startOfPeriod("month", now), currency)
	if err != nil {
		return 0, 0, 0, err
	}
	yearly, err := h.loadOrgSpend(ctx, nil, org.ID(), startOfPeriod("year", now), currency)
	if err != nil {
		return 0, 0, 0, err
	}
	return daily, monthly, yearly, nil
}

// loadMemberScopeSpend narrows the org spend to a single member.
func (h *Handler) loadMemberScopeSpend(ctx context.Context, org model.Organization, membership model.Membership, currency string) (int64, int64, int64, error) {
	now := time.Now()
	userIDs := []model.UserID{membership.UserID()}
	daily, err := h.loadOrgSpend(ctx, userIDs, org.ID(), startOfPeriod("day", now), currency)
	if err != nil {
		return 0, 0, 0, err
	}
	monthly, err := h.loadOrgSpend(ctx, userIDs, org.ID(), startOfPeriod("month", now), currency)
	if err != nil {
		return 0, 0, 0, err
	}
	yearly, err := h.loadOrgSpend(ctx, userIDs, org.ID(), startOfPeriod("year", now), currency)
	if err != nil {
		return 0, 0, 0, err
	}
	return daily, monthly, yearly, nil
}

// loadApplicationScopeSpend reads the application spend via applicationSpend.
func loadApplicationScopeSpend(ctx context.Context, usageStore port.UsageStore, orgID model.OrgID, appID model.ApplicationID, currency string) (int64, int64, int64, error) {
	now := time.Now()
	daily, err := applicationSpend(ctx, usageStore, appID, orgID, model.StartOfDay(now))
	if err != nil {
		return 0, 0, 0, err
	}
	monthly, err := applicationSpend(ctx, usageStore, appID, orgID, model.StartOfMonth(now))
	if err != nil {
		return 0, 0, 0, err
	}
	yearly, err := applicationSpend(ctx, usageStore, appID, orgID, model.StartOfYear(now))
	if err != nil {
		return 0, 0, 0, err
	}
	return daily, monthly, yearly, nil
}

// quotaFormConfig captures everything that varies between the three quota
// editors, so renderQuotaFormError can serve all three without
// duplication. The fields are read once at the call site and never
// mutated; passing the struct by value keeps the helper pure with respect
// to its inputs.
type quotaFormConfig struct {
	Scope        model.QuotaScope
	ScopeID      string
	Membership   model.Membership  // optional
	Application  model.Application // optional
	Breadcrumbs  []common.BreadcrumbItem
	SelectedItem string
	SpendLoader  quotaSpendLoader
	LoadLog      string // describes the scope in log lines, e.g. "member budget"
	// Existing is the quota already loaded by the save handler for
	// parseQuotaBudgetFields. Passing it through avoids a second
	// quotaStore.GetQuota read in the re-render path. nil is a valid
	// value meaning "no row on file" and is rendered as such by the
	// template.
	Existing model.Quota
}

// renderQuotaFormError re-renders the quota editor with HTTP 422 when
// validation fails on POST. The page carries the operator's submitted
// values, per-field errors and the existing budget/spend figures so the
// "?success=saved" redirect is never reached on a rejected submit
// (issue #88). Quota-load and spend-load failures are reported
// separately and combined into a single banner, matching the GET path:
// dropping one to surface the other would hide a real store failure
// from the operator.
func (h *Handler) renderQuotaFormError(w http.ResponseWriter, r *http.Request, ctx context.Context, user model.User, org model.Organization, cfg quotaFormConfig, fieldErrors map[string]string) {
	// The save handler already loaded the existing quota for
	// parseQuotaBudgetFields; we reuse it here so a rejected submit
	// makes one quotaStore read, not two. The spend side still queries
	// the usage store fresh because it is not loaded on the save path.
	existing := cfg.Existing

	orgCurrency := org.Currency()
	if orgCurrency == "" {
		orgCurrency = model.DefaultCurrency
	}
	dailyCost, monthlyCost, yearlyCost, spendErr := cfg.SpendLoader(ctx, orgCurrency)
	spendLoadError := ""
	if spendErr != nil {
		slog.ErrorContext(ctx, "could not load "+cfg.LoadLog+" spend for re-render", slogx.Error(spendErr))
		spendLoadError = "Consommation indisponible : le store a renvoyé une erreur. Les barres peuvent afficher 0 %."
	}
	loadError := ""
	if spendLoadError != "" {
		loadError = spendLoadError
	}

	scopeType := ""
	switch cfg.Scope {
	case model.QuotaScopeOrg:
		scopeType = "org"
	case model.QuotaScopeUser:
		scopeType = "user"
	case model.QuotaScopeApplication:
		scopeType = "application"
	}

	vmodel := component.QuotaPageVModel{
		Org:         org,
		Membership:  cfg.Membership,
		Application: cfg.Application,
		ScopeType:   scopeType,
		ScopeID:     cfg.ScopeID,
		Quota:       existing,
		DailyCost:   dailyCost,
		MonthlyCost: monthlyCost,
		YearlyCost:  yearlyCost,
		LoadError:   loadError,
		Submitted:   quotaFormSubmitted(r),
		FieldErrors: fieldErrors,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: cfg.SelectedItem,
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs:  cfg.Breadcrumbs,
		},
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	templ.Handler(component.QuotaPage(vmodel)).ServeHTTP(w, r)
}

// renderOrgQuotaFormError re-renders the org quota editor when validation
// fails on POST. See renderQuotaFormError for the shared body.
func (h *Handler) renderOrgQuotaFormError(w http.ResponseWriter, r *http.Request, ctx context.Context, user model.User, orgSlug string, org model.Organization, existing model.Quota, fieldErrors map[string]string) {
	h.renderQuotaFormError(w, r, ctx, user, org, quotaFormConfig{
		Scope:        model.QuotaScopeOrg,
		ScopeID:      string(org.ID()),
		SelectedItem: "org-" + orgSlug + "-quota",
		LoadLog:      "org budget",
		SpendLoader: func(ctx context.Context, currency string) (int64, int64, int64, error) {
			return h.loadOrgScopeSpend(ctx, org, currency)
		},
		Breadcrumbs: []common.BreadcrumbItem{
			{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
			{Label: "Budget", Href: "/orgs/" + orgSlug + "/admin/quota"},
		},
		Existing: existing,
	}, fieldErrors)
}

// renderMemberQuotaFormError re-renders the per-member quota editor when
// validation fails on POST. resolveOrgAndMembership is the only safe way
// for callers to obtain the membership here: GetMembership is keyed by
// membership id alone, so a foreign membership would let an operator
// rewrite another org's per-user budget.
func (h *Handler) renderMemberQuotaFormError(w http.ResponseWriter, r *http.Request, ctx context.Context, user model.User, orgSlug string, org model.Organization, membership model.Membership, existing model.Quota, fieldErrors map[string]string) {
	h.renderQuotaFormError(w, r, ctx, user, org, quotaFormConfig{
		Scope:        model.QuotaScopeUser,
		ScopeID:      string(membership.UserID()),
		Membership:   membership,
		SelectedItem: "org-" + orgSlug + "-members",
		LoadLog:      "member budget",
		SpendLoader: func(ctx context.Context, currency string) (int64, int64, int64, error) {
			return h.loadMemberScopeSpend(ctx, org, membership, currency)
		},
		Breadcrumbs: []common.BreadcrumbItem{
			{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
			{Label: "Membres", Href: "/orgs/" + orgSlug + "/admin/members"},
			{Label: membership.User().DisplayName(), Href: ""},
			{Label: "Budget", Href: ""},
		},
		Existing: existing,
	}, fieldErrors)
}

// renderApplicationQuotaFormError re-renders the per-application quota
// editor when validation fails on POST (issue #64).
func (h *Handler) renderApplicationQuotaFormError(w http.ResponseWriter, r *http.Request, ctx context.Context, user model.User, orgSlug string, org model.Organization, app model.Application, existing model.Quota, fieldErrors map[string]string) {
	appID := string(app.ID())
	h.renderQuotaFormError(w, r, ctx, user, org, quotaFormConfig{
		Scope:        model.QuotaScopeApplication,
		ScopeID:      appID,
		Application:  app,
		SelectedItem: "org-" + orgSlug + "-applications",
		LoadLog:      "application budget",
		SpendLoader: func(ctx context.Context, currency string) (int64, int64, int64, error) {
			return loadApplicationScopeSpend(ctx, h.usageStore, org.ID(), model.ApplicationID(appID), currency)
		},
		Breadcrumbs: []common.BreadcrumbItem{
			{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
			{Label: "Applications", Href: "/orgs/" + orgSlug + "/admin/applications"},
			{Label: app.Name(), Href: "/orgs/" + orgSlug + "/admin/applications/" + appID + "/edit"},
			{Label: "Budget", Href: ""},
		},
		Existing: existing,
	}, fieldErrors)
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

// maxBudgetUnits is the largest amount, in whole currency units,
// parseBudgetField will accept. The parser multiplies the parsed value
// by 1_000_000 to produce microcents, so the bound is the floor of
// math.MaxInt64 / 1_000_000. Without it, an out-of-range float converts
// to an arbitrary int64 (the Go spec leaves float→int64 out-of-range
// conversion implementation-defined), which a downstream store would
// happily keep.
const maxBudgetUnits = math.MaxInt64 / 1_000_000

// parseBudgetField parses a currency budget field into microcents.
//
// Return values:
//
//   - Empty → (nil, nil): unlimited, the historical default.
//
//   - "0" → (&0, nil): strict zero cap. The enforcer compares spent >= budget
//     and spent is always >= 0, so every PAYG request gets a 429. This is the
//     documented way to freeze an application or member immediately, and the
//     reason the field accepts 0 in the first place: the MoneyInput renders
//     with min="0", so removing that value would also reject the legitimate
//     freeze case.
//
//   - Anything else → (*int64, nil) on success, (nil, error) on a value that
//     cannot be trusted. The caller is expected to surface the error on the
//     form rather than silently storing "no cap" (the previous parser's
//     behaviour, which is what issue #88 is the fix for).
//
//   - Negative values, NaN, ±Inf and unparsable strings are rejected. A value
//     larger than maxBudgetUnits is also rejected with a clear message: a
//     hand-crafted value just over the cap would otherwise overflow the
//     microcent conversion and store an arbitrary amount.
func parseBudgetField(v string) (*int64, error) {
	if v == "" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil, fmt.Errorf("valeur invalide %q : nombre attendu (ex. 10.50)", v)
	}
	if math.IsNaN(f) {
		return nil, errors.New("valeur invalide : NaN n'est pas un plafond acceptable")
	}
	if math.IsInf(f, 0) {
		return nil, errors.New("valeur invalide : un nombre infini n'est pas un plafond acceptable")
	}
	if f < 0 {
		return nil, fmt.Errorf("valeur invalide %q : le plafond doit être positif ou nul", v)
	}
	if f > float64(maxBudgetUnits) {
		return nil, fmt.Errorf("valeur invalide %q : le plafond dépasse le maximum supporté (%d)", v, maxBudgetUnits)
	}
	mc := int64(f * 1_000_000)
	return &mc, nil
}
