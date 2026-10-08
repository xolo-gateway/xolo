package org

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/core/eventql"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
)

// alertPerms resolves the current user's alert capabilities within the org:
// managing org-wide alerts (events:write) and/or creating personal alerts
// (events:alerts:own).
func (h *Handler) alertPerms(ctx context.Context, orgSlug string, user model.User) (canWriteOrg, canOwnAlerts bool) {
	canWriteOrg, _ = h.hasPermission(orgSlug, rbac.PermEventsWrite)(ctx, user)
	canOwnAlerts, _ = h.hasPermission(orgSlug, rbac.PermEventsAlertsOwn)(ctx, user)
	return canWriteOrg, canOwnAlerts
}

// canManageAlert reports whether the user may view/edit/delete the given alert:
// org-scoped alerts require events:write; personal alerts belong to their owner.
func canManageAlert(alert model.Alert, user model.User, canWriteOrg bool) bool {
	if alert.Scope() == model.AlertScopePersonal {
		return user != nil && alert.OwnerID() == user.ID()
	}
	return canWriteOrg
}

// alertsRedirectURL builds the post-action redirect to the alerts list,
// preserving the browsing context.
func alertsRedirectURL(orgSlug, success string, r *http.Request) string {
	u := "/orgs/" + orgSlug + "/events/alerts?success=" + success
	if personalView(r) {
		u += "&view=personal"
	}
	return u
}

func (h *Handler) getAlertsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	all, err := h.alertStore.ListAlerts(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list alerts", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Only show alerts the user can manage: org-wide alerts (events:write) and
	// their own personal alerts.
	canWriteOrg, canOwnAlerts := h.alertPerms(ctx, orgSlug, user)
	alerts := make([]model.Alert, 0, len(all))
	for _, a := range all {
		if canManageAlert(a, user, canWriteOrg) {
			alerts = append(alerts, a)
		}
	}

	personal := personalView(r)
	vmodel := component.AlertsPageVModel{
		Org:             org,
		Alerts:          alerts,
		CanWriteOrg:     canWriteOrg,
		CanOwnAlerts:    canOwnAlerts,
		View:            viewParam(personal),
		Success:         r.URL.Query().Get("success"),
		AppLayoutVModel: h.eventsLayout(org, user, personal, eventsNavAlerts, []common.BreadcrumbItem{{Label: "Alertes", Href: ""}}),
	}

	templ.Handler(component.AlertsPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) getNewAlertPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	vmodel := h.newAlertFormVModel(ctx, org, user, orgSlug, personalView(r), nil)
	templ.Handler(component.AlertFormPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) getEditAlertPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	alertID := model.AlertID(r.PathValue("alertID"))

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	alert, err := h.alertStore.GetAlertByID(ctx, alertID)
	if err != nil || alert.OrgID() != org.ID() {
		http.Error(w, "Alert not found", http.StatusNotFound)
		return
	}

	canWriteOrg, _ := h.alertPerms(ctx, orgSlug, user)
	if !canManageAlert(alert, user, canWriteOrg) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	vmodel := h.newAlertFormVModel(ctx, org, user, orgSlug, personalView(r), alert)
	templ.Handler(component.AlertFormPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) createAlert(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	// Users with org-wide rights create org alerts; otherwise the alert is
	// personal and scoped to the creator's own events.
	canWriteOrg, canOwnAlerts := h.alertPerms(ctx, orgSlug, user)
	if !canWriteOrg && !canOwnAlerts {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	scope := model.AlertScopePersonal
	if canWriteOrg {
		scope = model.AlertScopeOrg
	}

	form, parseErr := parseAlertForm(r)
	if parseErr != "" {
		vmodel := h.newAlertFormVModel(ctx, org, user, orgSlug, personalView(r), nil)
		applyFormToVModel(&vmodel, r, parseErr)
		w.WriteHeader(http.StatusBadRequest)
		templ.Handler(component.AlertFormPage(vmodel)).ServeHTTP(w, r)
		return
	}

	alert := model.NewAlert(org.ID(), user.ID(), form.name,
		model.WithAlertScope(scope),
		model.WithAlertDescription(form.description),
		model.WithAlertQuery(form.query),
		model.WithAlertWindow(form.window),
		model.WithAlertComparator(form.comparator),
		model.WithAlertThreshold(form.threshold),
		model.WithAlertFor(form.forDuration),
		model.WithAlertEnabled(form.enabled),
	)

	if err := h.alertStore.CreateAlert(ctx, alert); err != nil {
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not create alert", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, alertsRedirectURL(orgSlug, "created", r), http.StatusSeeOther)
}

func (h *Handler) updateAlert(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	alertID := model.AlertID(r.PathValue("alertID"))

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	alert, err := h.alertStore.GetAlertByID(ctx, alertID)
	if err != nil || alert.OrgID() != org.ID() {
		http.Error(w, "Alert not found", http.StatusNotFound)
		return
	}

	canWriteOrg, _ := h.alertPerms(ctx, orgSlug, user)
	if !canManageAlert(alert, user, canWriteOrg) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	form, parseErr := parseAlertForm(r)
	if parseErr != "" {
		vmodel := h.newAlertFormVModel(ctx, org, user, orgSlug, personalView(r), alert)
		applyFormToVModel(&vmodel, r, parseErr)
		w.WriteHeader(http.StatusBadRequest)
		templ.Handler(component.AlertFormPage(vmodel)).ServeHTTP(w, r)
		return
	}

	// Editing the rule resets its evaluation state.
	updated := model.UpdateAlert(alert,
		model.WithAlertName(form.name),
		model.WithAlertDescription(form.description),
		model.WithAlertQuery(form.query),
		model.WithAlertWindow(form.window),
		model.WithAlertComparator(form.comparator),
		model.WithAlertThreshold(form.threshold),
		model.WithAlertFor(form.forDuration),
		model.WithAlertEnabled(form.enabled),
	)
	updated.SetState(model.AlertStateOK)
	updated.SetPendingSince(nil)

	if err := h.alertStore.UpdateAlert(ctx, updated); err != nil {
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not update alert", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, alertsRedirectURL(orgSlug, "updated", r), http.StatusSeeOther)
}

func (h *Handler) deleteAlert(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")
	alertID := model.AlertID(r.PathValue("alertID"))

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	alert, err := h.alertStore.GetAlertByID(ctx, alertID)
	if err != nil || alert.OrgID() != org.ID() {
		http.Error(w, "Alert not found", http.StatusNotFound)
		return
	}

	canWriteOrg, _ := h.alertPerms(ctx, orgSlug, user)
	if !canManageAlert(alert, user, canWriteOrg) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := h.alertStore.DeleteAlert(ctx, alertID); err != nil {
		if webcommon.RejectOwnership(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not delete alert", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, alertsRedirectURL(orgSlug, "deleted", r), http.StatusSeeOther)
}

// alertFormValues holds the parsed and validated alert form.
type alertFormValues struct {
	name        string
	description string
	query       string
	window      time.Duration
	comparator  model.AlertComparator
	threshold   float64
	forDuration time.Duration
	enabled     bool
}

func parseAlertForm(r *http.Request) (alertFormValues, string) {
	if err := r.ParseForm(); err != nil {
		return alertFormValues{}, "Formulaire invalide."
	}

	var v alertFormValues
	v.name = r.FormValue("name")
	if v.name == "" {
		return v, "Le nom est obligatoire."
	}
	v.description = r.FormValue("description")

	v.query = r.FormValue("query")
	if _, err := eventql.Compile(v.query); err != nil {
		return v, "Requête invalide : " + err.Error()
	}

	window, err := time.ParseDuration(r.FormValue("window"))
	if err != nil || window <= 0 {
		return v, "Fenêtre invalide (ex : 5m, 1h)."
	}
	v.window = window

	v.comparator = model.AlertComparator(r.FormValue("comparator"))
	switch v.comparator {
	case model.ComparatorGT, model.ComparatorGTE, model.ComparatorLT, model.ComparatorLTE, model.ComparatorEQ:
	default:
		return v, "Comparateur invalide."
	}

	threshold, err := strconv.ParseFloat(r.FormValue("threshold"), 64)
	if err != nil {
		return v, "Seuil invalide."
	}
	v.threshold = threshold

	forStr := r.FormValue("for")
	if forStr == "" {
		v.forDuration = 0
	} else {
		forDuration, err := time.ParseDuration(forStr)
		if err != nil || forDuration < 0 {
			return v, "Durée « pending » invalide (ex : 0s, 1m)."
		}
		v.forDuration = forDuration
	}

	v.enabled = r.FormValue("enabled") == "on"

	return v, ""
}

func (h *Handler) newAlertFormVModel(ctx context.Context, org model.Organization, user model.User, orgSlug string, personal bool, alert model.Alert) component.AlertFormVModel {
	// Determine the alert scope shown in the form: existing alerts keep their
	// scope; new alerts are org-wide for users with events:write, personal
	// otherwise.
	scope := model.AlertScopePersonal
	if alert != nil {
		scope = alert.Scope()
	} else if canWriteOrg, _ := h.alertPerms(ctx, orgSlug, user); canWriteOrg {
		scope = model.AlertScopeOrg
	}

	label := "Nouvelle alerte"
	if alert != nil {
		label = alert.Name()
	}

	vmodel := component.AlertFormVModel{
		Org:            org,
		Alert:          alert,
		IsNew:          alert == nil,
		Scope:          scope,
		View:           viewParam(personal),
		FormWindow:     "5m",
		FormComparator: string(model.ComparatorGT),
		FormFor:        "0s",
		FormEnabled:    true,
		AppLayoutVModel: h.eventsLayout(org, user, personal, eventsNavAlerts, []common.BreadcrumbItem{
			{Label: "Alertes", Href: "/orgs/" + orgSlug + "/events/alerts"},
			{Label: label, Href: ""},
		}),
	}

	if alert != nil {
		vmodel.FormName = alert.Name()
		vmodel.FormDescription = alert.Description()
		vmodel.FormQuery = alert.Query()
		vmodel.FormWindow = alert.Window().String()
		vmodel.FormComparator = string(alert.Comparator())
		vmodel.FormThreshold = strconv.FormatFloat(alert.Threshold(), 'f', -1, 64)
		vmodel.FormFor = alert.For().String()
		vmodel.FormEnabled = alert.Enabled()
	}

	return vmodel
}

// applyFormToVModel repopulates the form vmodel from the raw request on error.
func applyFormToVModel(vmodel *component.AlertFormVModel, r *http.Request, errMsg string) {
	vmodel.Error = errMsg
	vmodel.FormName = r.FormValue("name")
	vmodel.FormDescription = r.FormValue("description")
	vmodel.FormQuery = r.FormValue("query")
	vmodel.FormWindow = r.FormValue("window")
	vmodel.FormComparator = r.FormValue("comparator")
	vmodel.FormThreshold = r.FormValue("threshold")
	vmodel.FormFor = r.FormValue("for")
	vmodel.FormEnabled = r.FormValue("enabled") == "on"
}
