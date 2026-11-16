package org

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// SetEnrollment enables the pages of the enrollment rules.
func (h *Handler) SetEnrollment(enrollment *service.EnrollmentService) {
	h.enrollment = enrollment
}

// enrollmentOrg resolves the organization of a page of the enrollment rules.
func (h *Handler) enrollmentOrg(w http.ResponseWriter, r *http.Request) (model.Organization, bool) {
	ctx := r.Context()
	if h.enrollment == nil {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return nil, false
	}
	org, err := h.orgFromSlug(ctx, r.PathValue("orgSlug"))
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return nil, false
	}
	return org, true
}

func (h *Handler) enrollmentLayout(ctx context.Context, org model.Organization, page string) common.AppLayoutVModel {
	breadcrumbs := []common.BreadcrumbItem{
		{Label: org.Name(), Href: "/orgs/" + org.Slug() + "/usage"},
		{Label: i18n.T(ctx, "org.nav.enrollment"), Href: "/orgs/" + org.Slug() + "/admin/enrollment-rules"},
	}
	if page != "" {
		breadcrumbs = append(breadcrumbs, common.BreadcrumbItem{Label: page})
	} else {
		breadcrumbs[1].Href = ""
	}
	return common.AppLayoutVModel{
		User:         httpCtx.User(ctx),
		SelectedItem: "org-" + org.Slug() + "-enrollment",
		Context:      common.ContextOrg,
		ContextName:  org.Name(),
		ContextSlug:  org.Slug(),
		ContextOrgID: org.ID(),
		Breadcrumbs:  breadcrumbs,
	}
}

func (h *Handler) getEnrollmentRulesPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, ok := h.enrollmentOrg(w, r)
	if !ok {
		return
	}
	rules, err := h.enrollment.ListRules(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list enrollment rules", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	roles, err := h.roleStore.ListOrgRoles(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list org roles", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	names := make(map[string]string, len(roles))
	for _, role := range roles {
		label := role.Name()
		if role.BuiltinKind() != "" {
			label = component.BuiltinRoleLabel(ctx, role.BuiltinKind())
		}
		names[string(role.ID())] = label
		switch role.BuiltinKind() {
		case model.BuiltinKindMember:
			names[model.RoleMember] = label
		case model.BuiltinKindAdmin:
			names[model.RoleOrgAdmin] = label
		}
	}
	canWrite := common.HasPermission(ctx, org.ID(), rbac.PermInvitesWrite)
	templ.Handler(component.EnrollmentRulesPage(component.EnrollmentRulesPageVModel{
		AppLayoutVModel: h.enrollmentLayout(ctx, org, ""),
		Org:             org,
		Rules:           rules,
		RoleName:        func(value string) string { return names[value] },
		CanWrite:        canWrite,
		Success:         r.URL.Query().Get("success"),
	})).ServeHTTP(w, r)
}

// enrollmentForm is the form of a rule, filled from rule when it is not nil.
func (h *Handler) enrollmentForm(ctx context.Context, org model.Organization, title string, rule model.EnrollmentRule) (component.EnrollmentRuleFormVModel, error) {
	roles, err := h.roleStore.ListOrgRoles(ctx, org.ID())
	if err != nil {
		return component.EnrollmentRuleFormVModel{}, errors.WithStack(err)
	}
	vmodel := component.EnrollmentRuleFormVModel{
		AppLayoutVModel: h.enrollmentLayout(ctx, org, title),
		Org:             org,
		Kind:            model.EnrollmentKindEmailDomain,
		Enabled:         true,
		OrgRoles:        roles,
	}
	if rule != nil {
		vmodel.RuleID = rule.ID()
		vmodel.Kind = rule.Kind()
		vmodel.Name = rule.Name()
		vmodel.Claim = rule.Claim()
		vmodel.Value = rule.Value()
		vmodel.Role = rule.Role()
		vmodel.Enabled = rule.Enabled()
	}
	return vmodel, nil
}

func (h *Handler) renderEnrollmentForm(w http.ResponseWriter, r *http.Request, vmodel component.EnrollmentRuleFormVModel) {
	templ.Handler(component.EnrollmentRuleFormPage(vmodel)).ServeHTTP(w, r)
}

// enrollmentRuleForm holds the values posted by the rule form.
type enrollmentRuleForm struct {
	name    string
	kind    model.EnrollmentKind
	claim   string
	value   string
	role    string
	enabled bool
}

func parseEnrollmentRuleForm(r *http.Request) (enrollmentRuleForm, error) {
	if err := r.ParseForm(); err != nil {
		return enrollmentRuleForm{}, errors.WithStack(err)
	}
	form := enrollmentRuleForm{
		name:    r.PostFormValue("name"),
		kind:    model.EnrollmentKind(r.PostFormValue("kind")),
		claim:   r.PostFormValue("claim"),
		value:   r.PostFormValue("value"),
		role:    r.PostFormValue("role"),
		enabled: r.PostFormValue("enabled") != "",
	}
	if form.kind == model.EnrollmentKindEmailDomain {
		form.claim = ""
	}
	return form, nil
}

func (h *Handler) getNewEnrollmentRulePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, ok := h.enrollmentOrg(w, r)
	if !ok {
		return
	}
	vmodel, err := h.enrollmentForm(ctx, org, i18n.T(ctx, "org.enrollment.new"), nil)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}
	h.renderEnrollmentForm(w, r, vmodel)
}

func (h *Handler) getEditEnrollmentRulePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, ok := h.enrollmentOrg(w, r)
	if !ok {
		return
	}
	rule, err := h.enrollment.GetRule(ctx, org.ID(), model.EnrollmentRuleID(r.PathValue("ruleID")))
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}
	vmodel, err := h.enrollmentForm(ctx, org, rule.Name(), rule)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}
	h.renderEnrollmentForm(w, r, vmodel)
}

func (h *Handler) createEnrollmentRule(w http.ResponseWriter, r *http.Request) {
	org, ok := h.enrollmentOrg(w, r)
	if !ok {
		return
	}
	form, err := parseEnrollmentRuleForm(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	rule := model.NewEnrollmentRule(org.ID(), form.name, form.kind, form.claim, form.value, form.role, form.enabled)
	h.saveEnrollmentRule(w, r, org, rule, true)
}

func (h *Handler) updateEnrollmentRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, ok := h.enrollmentOrg(w, r)
	if !ok {
		return
	}
	current, err := h.enrollment.GetRule(ctx, org.ID(), model.EnrollmentRuleID(r.PathValue("ruleID")))
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}
	form, err := parseEnrollmentRuleForm(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	rule := model.UpdateEnrollmentRule(current,
		model.WithEnrollmentRuleName(form.name),
		model.WithEnrollmentRuleKind(form.kind),
		model.WithEnrollmentRuleClaim(form.claim),
		model.WithEnrollmentRuleValue(form.value),
		model.WithEnrollmentRuleRole(form.role),
		model.WithEnrollmentRuleEnabled(form.enabled),
	)
	h.saveEnrollmentRule(w, r, org, rule, false)
}

func (h *Handler) saveEnrollmentRule(w http.ResponseWriter, r *http.Request, org model.Organization, rule model.EnrollmentRule, create bool) {
	ctx := r.Context()
	err := h.enrollment.SaveRule(ctx, org.TenantID(), rule, create)
	switch {
	case err == nil:
		success := "updated"
		if create {
			success = "created"
		}
		http.Redirect(w, r, "/orgs/"+org.Slug()+"/admin/enrollment-rules?success="+success, http.StatusSeeOther)
	case errors.Is(err, port.ErrInvalid):
		title := rule.Name()
		if create {
			title = i18n.T(ctx, "org.enrollment.new")
		}
		vmodel, formErr := h.enrollmentForm(ctx, org, title, rule)
		if formErr != nil {
			writeResourceLookupError(ctx, w, formErr)
			return
		}
		if create {
			vmodel.RuleID = ""
		}
		vmodel.Error = i18n.T(ctx, "org.enrollment.form.invalid")
		w.WriteHeader(http.StatusBadRequest)
		h.renderEnrollmentForm(w, r, vmodel)
	case webcommon.RejectLocked(w, r, err):
	default:
		writeResourceLookupError(ctx, w, err)
	}
}

func (h *Handler) deleteEnrollmentRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, ok := h.enrollmentOrg(w, r)
	if !ok {
		return
	}
	err := h.enrollment.DeleteRule(ctx, org.TenantID(), org.ID(), model.EnrollmentRuleID(r.PathValue("ruleID")))
	if err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		writeResourceLookupError(ctx, w, err)
		return
	}
	http.Redirect(w, r, "/orgs/"+org.Slug()+"/admin/enrollment-rules?success=deleted", http.StatusSeeOther)
}
