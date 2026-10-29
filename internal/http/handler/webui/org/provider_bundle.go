package org

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/crypto"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// maxProviderBundleSize bounds an uploaded bundle. A provider with a few
// hundred models stays well under it. It is the only bound on the number of
// models and the size of their extra_body, as the model form has none: a
// bundle holds nothing the form could not write one model at a time.
const maxProviderBundleSize = 4 << 20

// maxProviderBundleFormSize bounds the form that posts the bundle back with
// the selection: URL encoding can triple its size.
const maxProviderBundleFormSize = 3*maxProviderBundleSize + 1<<20

// exportProviderBundle downloads the provider and its models as a bundle.
// The API key and the internal IDs stay out of it.
func (h *Handler) exportProviderBundle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	org, p, err := h.resolveOrgAndProvider(ctx, r.PathValue("orgSlug"), r.PathValue("providerID"))
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}
	models, err := h.providerStore.ListLLMModels(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not list models to export", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	bundle := model.NewProviderBundle(p, models, r.URL.Query().Get("enabled_only") == "1", time.Now())
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		slog.ErrorContext(ctx, "could not encode provider bundle", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+bundleFileName(p.Name())+`"`)
	_, _ = w.Write(data)
}

var unsafeFileNameChars = regexp.MustCompile(`[^a-z0-9]+`)

// bundleFileName names the downloaded bundle after the provider.
func bundleFileName(providerName string) string {
	name := strings.Trim(unsafeFileNameChars.ReplaceAllString(strings.ToLower(providerName), "-"), "-")
	if name == "" {
		name = "provider"
	}
	return name + ".provider.json"
}

func (h *Handler) getProviderBundleImportPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, p, ok := h.resolveProviderBundleTarget(w, r)
	if !ok {
		return
	}
	templ.Handler(component.ProviderBundleImportPage(h.providerBundleImportVModel(ctx, org, p))).ServeHTTP(w, r)
}

// importProviderBundle runs one step of an import: "preview" reads the
// uploaded bundle and renders its dry run, "apply" plans the posted bundle
// again against the current state and writes the selected changes.
func (h *Handler) importProviderBundle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, p, ok := h.resolveProviderBundleTarget(w, r)
	if !ok {
		return
	}

	vmodel := h.providerBundleImportVModel(ctx, org, p)

	var raw []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxProviderBundleSize+1<<20)
		if err := r.ParseMultipartForm(maxProviderBundleSize); err != nil {
			h.renderProviderBundleError(w, r, vmodel, bundleReadError(ctx, err))
			return
		}
		file, _, err := r.FormFile("bundle_file")
		if err != nil {
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.provider_bundle.no_file"))
			return
		}
		defer file.Close()
		// One byte over the limit tells a file too large from a truncated one.
		raw, err = io.ReadAll(io.LimitReader(file, maxProviderBundleSize+1))
		if err != nil {
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.provider_bundle.unreadable"))
			return
		}
		if len(raw) > maxProviderBundleSize {
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.provider_bundle.too_large"))
			return
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, maxProviderBundleFormSize)
		if err := r.ParseForm(); err != nil {
			h.renderProviderBundleError(w, r, vmodel, bundleReadError(ctx, err))
			return
		}
		raw = []byte(r.FormValue("bundle"))
	}

	bundle, err := model.ParseProviderBundle(raw)
	if err == nil {
		// The rules of every provider write path, which the apply step
		// checks: the dry run offers nothing the apply would refuse.
		err = service.ValidateBundleSettings(bundle.Provider)
	}
	if err != nil {
		h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.provider_bundle.invalid", err.Error()))
		return
	}

	parsedCurrency := bundle.Provider.Currency
	// A bundle priced in another currency than the provider it is imported
	// into is converted, so that its prices and budgets never land under a
	// currency they are not in.
	if p != nil && p.Currency() == "" {
		h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.providers.no_currency"))
		return
	}
	if p != nil && bundle.Provider.Currency != p.Currency() {
		if err := h.convertBundleCurrency(ctx, &bundle, p.Currency()); err != nil {
			slog.WarnContext(ctx, "could not convert bundle prices", slogx.Error(err))
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.provider_bundle.no_rate", bundle.Provider.Currency, p.Currency()))
			return
		}
		// Converted, a price may leave the bounds the apply step checks
		// again on the bundle posted back.
		if err := bundle.Validate(); err != nil {
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.provider_bundle.converted_invalid", p.Currency(), err.Error()))
			return
		}
	}

	// The apply step plans with the selection it applies, which may change
	// the API the models are served by.
	apply := r.FormValue("step") == "apply"
	var sel *service.BundleSelection
	if apply {
		selection := providerBundleSelection(r)
		sel = &selection
	}

	plan, err := h.planProviderBundleImport(ctx, org, p, bundle, sel)
	if err != nil {
		slog.ErrorContext(ctx, "could not plan provider bundle import", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	vmodel.Plan = &plan
	vmodel.Bundle = string(raw)
	if bundle.Provider.Currency != parsedCurrency {
		// The apply step writes what this dry run shows: it posts back the
		// converted bundle, not the file, so a rate that moves in between
		// changes nothing.
		converted, err := json.Marshal(bundle)
		if err != nil {
			slog.ErrorContext(ctx, "could not encode converted bundle", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		vmodel.Bundle = string(converted)
	}
	vmodel.Name = bundle.Provider.Name
	vmodel.OmittedHeaders = strings.Join(bundle.Provider.OmittedHeaders, ", ")

	if !apply {
		templ.Handler(component.ProviderBundleImportPage(vmodel)).ServeHTTP(w, r)
		return
	}

	var newProvider *service.NewProviderTarget
	if p == nil {
		name := strings.TrimSpace(r.FormValue("name"))
		apiKey := r.FormValue("api_key")
		vmodel.Name = name
		if name == "" {
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.provider_bundle.name_required"))
			return
		}
		if apiKey == "" {
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.providers.api_key_required"))
			return
		}
		if len(apiKey) > service.MaxProviderKeyLength {
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.providers.api_key_too_long", service.MaxProviderKeyLength))
			return
		}
		encryptedKey, err := crypto.Encrypt(h.secretKey, apiKey)
		if err != nil {
			slog.ErrorContext(ctx, "could not encrypt API key", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		newProvider = &service.NewProviderTarget{OrgID: org.ID(), Name: name, APIKey: encryptedKey}
	}

	result, err := service.ApplyProviderBundle(ctx, h.providerStore, plan, *sel, newProvider)
	if err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		if errors.Is(err, port.ErrInvalid) {
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.provider_bundle.invalid_settings"))
			return
		}
		slog.ErrorContext(ctx, "could not apply provider bundle", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if !result.Refused() && result.Wrote() {
		http.Redirect(w, r, "/orgs/"+org.Slug()+"/admin/providers/"+string(result.Provider.ID())+"/models?success=imported", http.StatusSeeOther)
		return
	}

	// Some changes were refused, or nothing was left to apply: show it on
	// the dry run of the bundle against the state the import left.
	if result.ProviderChanged {
		if p == nil {
			name := vmodel.Name
			omitted := vmodel.OmittedHeaders
			vmodel = h.providerBundleImportVModel(ctx, org, result.Provider)
			vmodel.Bundle, vmodel.Name, vmodel.OmittedHeaders = string(raw), name, omitted
		}
		// The dry run is replanned against the provider as written: its type
		// may have changed.
		p = result.Provider
	}
	replan, err := h.planProviderBundleImport(ctx, org, p, bundle, nil)
	if err != nil {
		slog.ErrorContext(ctx, "could not plan provider bundle import", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	vmodel.Plan = &replan
	switch {
	case !result.Refused():
		vmodel.Notice = i18n.T(ctx, "org.provider_bundle.nothing_applied")
	case !result.Wrote():
		vmodel.Notice = i18n.T(ctx, "org.provider_bundle.nothing_written")
	case result.ProviderChanged:
		vmodel.Notice = i18n.T(ctx, "org.provider_bundle.applied_with_provider", result.Created, result.Updated)
	default:
		vmodel.Notice = i18n.T(ctx, "org.provider_bundle.applied", result.Created, result.Updated)
	}
	if result.Refused() {
		vmodel.Failures = append(h.bundleFailureReasons(ctx, org, result.Failed), bundleSkipReasons(ctx, result.Skipped)...)
		for _, change := range result.SkippedSettings {
			if len(change.Unserved) > 0 {
				vmodel.Failures = append(vmodel.Failures, i18n.T(ctx, "org.provider_bundle.type_not_applied", strings.Join(change.Unserved, ", ")))
			}
		}
		if result.TypeUnchecked != nil {
			slog.ErrorContext(ctx, "could not check the imported provider type", slogx.Error(result.TypeUnchecked))
			vmodel.Failures = append(vmodel.Failures, i18n.T(ctx, "org.provider_bundle.type_not_checked"))
		}
		switch {
		case result.SettingsFailed == nil:
		case errors.Is(result.SettingsFailed, port.ErrOwnershipDenied) || errors.Is(result.SettingsFailed, port.ErrResourceDeleted):
			vmodel.Failures = append(vmodel.Failures, i18n.T(ctx, "org.provider_bundle.settings_locked"))
		default:
			slog.ErrorContext(ctx, "could not save imported provider settings", slogx.Error(result.SettingsFailed))
			vmodel.Failures = append(vmodel.Failures, i18n.T(ctx, "org.provider_bundle.settings_not_saved"))
		}
		if len(result.Stranded) > 0 {
			vmodel.Failures = append(vmodel.Failures, i18n.T(ctx, "org.provider_bundle.models_stranded", strings.Join(result.Stranded, ", ")))
		}
	}
	templ.Handler(component.ProviderBundleImportPage(vmodel)).ServeHTTP(w, r)
}

// bundleFailureReasons explains why the store refused each failed model:
// its proxy name taken since the dry run by another model, as by another
// admin, or a failure a retry may get through.
func (h *Handler) bundleFailureReasons(ctx context.Context, org model.Organization, failed []service.BundleModelFailure) []string {
	existing, err := h.providerStore.ListLLMModels(ctx, org.ID())
	holders := map[string]model.LLMModelID{}
	if err == nil {
		for _, m := range existing {
			holders[m.ProxyName()] = m.ID()
		}
	}
	reasons := make([]string, 0, len(failed))
	for _, f := range failed {
		slog.ErrorContext(ctx, "could not import bundle model", slog.String("model", f.ProxyName), slogx.Error(f.Err))
		// An updated model that kept its name holds it itself.
		if holder, taken := holders[f.ProxyName]; taken && holder != f.ModelID {
			reasons = append(reasons, i18n.T(ctx, "org.errors.model_import.skip_conflict", f.ProxyName, i18n.T(ctx, "org.errors.model_import.name_taken", f.ProxyName)))
			continue
		}
		reasons = append(reasons, i18n.T(ctx, "org.errors.model_import.skip_failed", f.ProxyName))
	}
	return reasons
}

// bundleSkipReasons explains why selected models could not be applied.
func bundleSkipReasons(ctx context.Context, skipped []service.BundleModelPlan) []string {
	reasons := make([]string, 0, len(skipped))
	for _, mp := range skipped {
		if mp.RefusedField != "" && mp.Existing != nil {
			// One change of a model otherwise updated: it is named under the
			// name it still has.
			reason := mp.Conflict
			if mp.KindNotServed != "" {
				reason = i18n.T(ctx, "org.errors.providers.kind_not_served", mp.KindNotServed, common.ModelKindLabel(ctx, mp.UnservedKind))
			}
			reasons = append(reasons, i18n.T(ctx, "org.provider_bundle.change_not_applied", mp.Existing.ProxyName(), component.ProviderBundleFieldLabel(ctx, mp.RefusedField), reason))
			continue
		}
		reason := mp.Conflict
		if mp.KindNotServed != "" {
			reason = i18n.T(ctx, "org.errors.providers.kind_not_served", mp.KindNotServed, common.ModelKindLabel(ctx, mp.Model.Kind))
		}
		reasons = append(reasons, i18n.T(ctx, "org.errors.model_import.skip_conflict", mp.Model.ProxyName, reason))
	}
	return reasons
}

// bundleReadError explains why the posted bundle could not be read.
func bundleReadError(ctx context.Context, err error) string {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return i18n.T(ctx, "org.errors.provider_bundle.too_large")
	}
	return i18n.T(ctx, "org.errors.provider_bundle.unreadable")
}

// resolveProviderBundleTarget resolves the organization and, on the import of
// an existing provider, the provider. It answers the request itself and
// returns false when they cannot be resolved.
func (h *Handler) resolveProviderBundleTarget(w http.ResponseWriter, r *http.Request) (model.Organization, model.Provider, bool) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")
	providerID := r.PathValue("providerID")
	if providerID == "" {
		org, err := h.orgFromSlug(ctx, orgSlug)
		if err != nil {
			http.Error(w, "Organization not found", http.StatusNotFound)
			return nil, nil, false
		}
		return org, nil, true
	}
	org, p, err := h.resolveOrgAndProvider(ctx, orgSlug, providerID)
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return nil, nil, false
	}
	return org, p, true
}

// planProviderBundleImport compares bundle with the provider p of org, or
// with nothing when the import creates a provider.
func (h *Handler) planProviderBundleImport(ctx context.Context, org model.Organization, p model.Provider, bundle model.ProviderBundle, sel *service.BundleSelection) (service.ProviderBundlePlan, error) {
	existing, err := h.providerStore.ListLLMModels(ctx, org.ID())
	if err != nil {
		return service.ProviderBundlePlan{}, errors.WithStack(err)
	}
	names, err := h.orgModelNames(ctx, org.ID(), existing)
	if err != nil {
		return service.ProviderBundlePlan{}, errors.WithStack(err)
	}
	return service.PlanProviderBundleImportWith(bundle, p, existing, &bundleProxyNames{ctx: ctx, names: names}, sel), nil
}

// bundleProxyNames checks the proxy names of a bundle import with the rules
// of the model form.
type bundleProxyNames struct {
	ctx   context.Context
	names *orgModelNames
}

func (n *bundleProxyNames) Conflict(proxyName string) string {
	return n.names.conflict(n.ctx, proxyName)
}
func (n *bundleProxyNames) Declare(proxyName string) { n.names.models[proxyName] = true }
func (n *bundleProxyNames) Release(proxyName string) { delete(n.names.models, proxyName) }

// providerBundleSelection reads the changes the operator selected.
func providerBundleSelection(r *http.Request) service.BundleSelection {
	sel := service.BundleSelection{
		Settings: map[string]bool{},
		Billing:  r.FormValue("billing") == "on",
		Models:   map[int]bool{},
		Fields:   map[int]map[string]bool{},
	}
	for _, field := range r.Form["setting"] {
		sel.Settings[field] = true
	}
	for _, value := range r.Form["create"] {
		if index, err := strconv.Atoi(value); err == nil {
			sel.Models[index] = true
		}
	}
	for _, value := range r.Form["field"] {
		rawIndex, field, found := strings.Cut(value, ":")
		index, err := strconv.Atoi(rawIndex)
		if !found || err != nil {
			continue
		}
		if sel.Fields[index] == nil {
			sel.Fields[index] = map[string]bool{}
		}
		sel.Fields[index][field] = true
	}
	return sel
}

func (h *Handler) renderProviderBundleError(w http.ResponseWriter, r *http.Request, vmodel component.ProviderBundleImportVModel, msg string) {
	h.renderProviderBundleErrorStatus(w, r, vmodel, msg, http.StatusUnprocessableEntity)
}

// renderProviderBundleErrorStatus renders the import page with an error
// answered under status.
func (h *Handler) renderProviderBundleErrorStatus(w http.ResponseWriter, r *http.Request, vmodel component.ProviderBundleImportVModel, msg string, status int) {
	vmodel.Error = msg
	w.WriteHeader(status)
	templ.Handler(component.ProviderBundleImportPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) providerBundleImportVModel(ctx context.Context, org model.Organization, p model.Provider) component.ProviderBundleImportVModel {
	orgSlug := org.Slug()
	breadcrumbs := []common.BreadcrumbItem{
		{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
		{Label: i18n.T(ctx, "org.nav.providers"), Href: "/orgs/" + orgSlug + "/admin/providers"},
	}
	if p != nil {
		breadcrumbs = append(breadcrumbs, common.BreadcrumbItem{Label: p.Name(), Href: "/orgs/" + orgSlug + "/admin/providers/" + string(p.ID()) + "/models"})
	}
	breadcrumbs = append(breadcrumbs, common.BreadcrumbItem{Label: i18n.T(ctx, "org.nav.import"), Href: ""})
	return component.ProviderBundleImportVModel{
		Org:      org,
		Provider: p,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         httpCtx.User(ctx),
			SelectedItem: "org-" + orgSlug + "-providers",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  orgSlug,
			ContextOrgID: org.ID(),
			Breadcrumbs:  breadcrumbs,
		},
	}
}

// convertBundleCurrency converts the prices and the plan's value budgets of
// bundle into the currency to, which becomes the bundle's.
func (h *Handler) convertBundleCurrency(ctx context.Context, bundle *model.ProviderBundle, to string) error {
	if h.exchangeRateService == nil {
		return errors.New("no exchange rate service")
	}
	from := bundle.Provider.Currency
	convert := func(microcents int64) (int64, error) {
		return h.exchangeRateService.Convert(ctx, microcents, from, to)
	}
	for i := range bundle.Models {
		prices := &bundle.Models[i].Prices
		for _, p := range []*float64{&prices.Input, &prices.CachedInput, &prices.Output} {
			v, err := convert(model.MicrocentsPer1K(*p))
			if err != nil {
				return errors.WithStack(err)
			}
			*p = model.PricePerMillion(v)
		}
	}
	if plan := bundle.Provider.SubscriptionPlan; plan != nil {
		for ci := range plan.Constraints {
			c := &plan.Constraints[ci]
			if c.ValueBudget != nil {
				v, err := convert(*c.ValueBudget)
				if err != nil {
					return errors.WithStack(err)
				}
				c.ValueBudget = &v
			}
			for id, b := range c.ModelBudgets {
				if b.ValueBudget == nil {
					continue
				}
				v, err := convert(*b.ValueBudget)
				if err != nil {
					return errors.WithStack(err)
				}
				b.ValueBudget = &v
				c.ModelBudgets[id] = b
			}
		}
	}
	bundle.Provider.Currency = to
	return nil
}
