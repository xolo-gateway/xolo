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
	daily, monthly, yearly, fieldErrors := parseQuotaBudgetFields(r)

	if len(fieldErrors) > 0 {
		// Re-render the form rather than 400 + redirect: an operator who
		// typed a bad value needs the page back with their input and the
		// reason it was rejected, not a generic error and certainly not a
		// ?success=saved banner that lies about the outcome (issue #88).
		h.renderOrgQuotaFormError(w, r, ctx, user, orgSlug, org, fieldErrors)
		return
	}

	quota := model.NewQuota(model.QuotaScopeOrg, string(org.ID()), currency, daily, monthly, yearly)
	if err := h.quotaStore.SetQuota(ctx, quota); err != nil {
		slog.ErrorContext(ctx, "could not save org quota", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/quota?success=saved", http.StatusSeeOther)
}

// membershipFromQuotaPath loads the org from the URL slug, then the
// membership from the URL id, and asserts the membership belongs to the
// org. Returns:
//
//   - org not found: orgFromSlug's error (typically port.ErrNotFound).
//     The caller answers 404 with "Organization not found".
//
//   - membership not found: h.orgStore.GetMembership's error (typically
//     port.ErrNotFound). The caller answers 404 with "Membership not
//     found".
//
//   - membership belongs to another org: wrapped port.ErrNotFound. The
//     caller answers 404 with "Membership not found" (the operator must
//     not learn whether the membership exists in another org of the
//     same tenant — this matches the resolveOrgAndApplication pattern
//     where a foreign app also looks like a not-found).
//
// What this check does NOT cover: a user who legitimately belongs to
// several orgs of the same tenant. QuotaStore keys user quotas by userID
// alone (no org column on the Quota model), so an admin of org A can
// still set or freeze the shared budget the enforcer applies to that
// user in org B. That is a model-level property of QuotaScopeUser and
// would need either (a) scoping Quota by (orgID, userID) end-to-end
// (model, store, service, enforcer, migration) or (b) refusing to edit
// a user quota from any membership when the user has memberships in
// other orgs. Both are out of scope for issue #88.
func (h *Handler) membershipFromQuotaPath(ctx context.Context, orgSlug, membershipID string) (model.Organization, model.Membership, error) {
	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		return nil, nil, err
	}
	membership, err := h.orgStore.GetMembership(ctx, model.MembershipID(membershipID))
	if err != nil {
		return nil, nil, err
	}
	if membership.OrgID() != org.ID() {
		return nil, nil, errors.WithStack(port.ErrNotFound)
	}
	return org, membership, nil
}

func (h *Handler) getMemberQuotaPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	membershipID := r.PathValue("membershipID")

	org, membership, err := h.membershipFromQuotaPath(ctx, orgSlug, membershipID)
	if err != nil {
		switch {
		case errors.Is(err, port.ErrNotFound) && org == nil:
			http.Error(w, "Organization not found", http.StatusNotFound)
			return
		case errors.Is(err, port.ErrNotFound):
			http.Error(w, "Membership not found", http.StatusNotFound)
			return
		}
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
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

	org, membership, err := h.membershipFromQuotaPath(ctx, orgSlug, membershipID)
	if err != nil {
		switch {
		case errors.Is(err, port.ErrNotFound) && org == nil:
			http.Error(w, "Organization not found", http.StatusNotFound)
			return
		case errors.Is(err, port.ErrNotFound):
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
	daily, monthly, yearly, fieldErrors := parseQuotaBudgetFields(r)

	if len(fieldErrors) > 0 {
		h.renderMemberQuotaFormError(w, r, ctx, user, orgSlug, org, membership, fieldErrors)
		return
	}

	quota := model.NewQuota(model.QuotaScopeUser, string(membership.UserID()), currency, daily, monthly, yearly)
	if err := h.quotaStore.SetQuota(ctx, quota); err != nil {
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
	daily, monthly, yearly, fieldErrors := parseQuotaBudgetFields(r)

	if len(fieldErrors) > 0 {
		h.renderApplicationQuotaFormError(w, r, ctx, user, orgSlug, org, app, fieldErrors)
		return
	}

	quota := model.NewQuota(model.QuotaScopeApplication, appID, currency, daily, monthly, yearly)
	if err := h.quotaStore.SetQuota(ctx, quota); err != nil {
		slog.ErrorContext(ctx, "could not save application quota", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/applications/"+appID+"/quota?success=saved", http.StatusSeeOther)
}

// parseQuotaBudgetFields runs parseBudgetField over the three budget inputs
// of the quota editor form. The first error wins per field, so a single
// field with two issues does not produce two messages; the whole map is
// still returned so the caller can highlight every offending input at once.
//
// A nil entry on the returned map means the field either parsed cleanly or
// was empty (unlimited). An absent key means the field had no input at all,
// which is the unlimited case for the form.
func parseQuotaBudgetFields(r *http.Request) (daily, monthly, yearly *int64, fieldErrors map[string]string) {
	daily, errD := parseBudgetField(r.FormValue("daily_budget"))
	monthly, errM := parseBudgetField(r.FormValue("monthly_budget"))
	yearly, errY := parseBudgetField(r.FormValue("yearly_budget"))
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
	existing, err := h.quotaStore.GetQuota(ctx, cfg.Scope, cfg.ScopeID)
	budgetLoadError := ""
	if err != nil && !errors.Is(err, port.ErrNotFound) {
		slog.ErrorContext(ctx, "could not load "+cfg.LoadLog+" for re-render", slogx.Error(err))
		budgetLoadError = "Budget indisponible : le store a renvoyé une erreur. Le formulaire ci-dessous reste éditable mais le total dépensé peut être inexact."
	}

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
	// Mirror the GET handlers' combine switch: if both stores fail, the
	// operator sees one banner that mentions both problems rather than
	// the second silently overwriting the first.
	loadError := budgetLoadError
	switch {
	case budgetLoadError != "" && spendLoadError != "":
		loadError = budgetLoadError + " " + spendLoadError
	case spendLoadError != "":
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
func (h *Handler) renderOrgQuotaFormError(w http.ResponseWriter, r *http.Request, ctx context.Context, user model.User, orgSlug string, org model.Organization, fieldErrors map[string]string) {
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
	}, fieldErrors)
}

// renderMemberQuotaFormError re-renders the per-member quota editor when
// validation fails on POST. membershipFromQuotaPath is the only safe way
// for callers to obtain the membership here: GetMembership is keyed by
// membership id alone, so a foreign membership would let an operator
// rewrite another org's per-user budget.
func (h *Handler) renderMemberQuotaFormError(w http.ResponseWriter, r *http.Request, ctx context.Context, user model.User, orgSlug string, org model.Organization, membership model.Membership, fieldErrors map[string]string) {
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
	}, fieldErrors)
}

// renderApplicationQuotaFormError re-renders the per-application quota
// editor when validation fails on POST (issue #64).
func (h *Handler) renderApplicationQuotaFormError(w http.ResponseWriter, r *http.Request, ctx context.Context, user model.User, orgSlug string, org model.Organization, app model.Application, fieldErrors map[string]string) {
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

// maxBudgetMicrocents is the largest microcent amount parseBudgetField will
// emit. It is math.MaxInt64 rounded down to the nearest whole unit (1 unit ==
// 1 000 000 microcents) so the int64 conversion below cannot overflow: the
// ticket's "1e16" example would silently become a small positive value without
// this clamp, because int64(1e16 * 1_000_000) wraps to a negative number that
// then rounds back into a positive one through the existing abs-style usage.
const maxBudgetMicrocents = math.MaxInt64 / 1_000_000

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
//     form rather than silently store a budget of 0, which is what the
//     previous parser did and what issue #88 is the fix for.
//
//   - Negative values, NaN, ±Inf and unparsable strings are rejected. A value
//     larger than maxBudgetMicrocents (math.MaxInt64 / 1_000_000, in whole
//     units) is also rejected with a clear message — without the cap, a
//     hand-crafted "1e16" silently overflowed int64 during the * 1_000_000
//     conversion and stored an arbitrary amount, which is worse than rejecting
//     the input outright.
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
	if f > float64(maxBudgetMicrocents) {
		return nil, fmt.Errorf("valeur invalide %q : le plafond dépasse le maximum supporté (%d)", v, maxBudgetMicrocents)
	}
	mc := int64(f * 1_000_000)
	return &mc, nil
}
