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
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/crypto"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	webcommon "github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// maxProviderBundleSize bounds an uploaded bundle. A provider with a few
// hundred models stays well under it.
const maxProviderBundleSize = 4 << 20

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

	r.Body = http.MaxBytesReader(w, r.Body, maxProviderBundleSize+1<<16)
	vmodel := h.providerBundleImportVModel(ctx, org, p)

	var raw []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(maxProviderBundleSize); err != nil {
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.provider_bundle.unreadable"))
			return
		}
		file, _, err := r.FormFile("bundle_file")
		if err != nil {
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.provider_bundle.no_file"))
			return
		}
		defer file.Close()
		raw, err = io.ReadAll(io.LimitReader(file, maxProviderBundleSize))
		if err != nil {
			h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.provider_bundle.unreadable"))
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", http.StatusBadRequest)
			return
		}
		raw = []byte(r.FormValue("bundle"))
	}

	bundle, err := model.ParseProviderBundle(raw)
	if err != nil {
		h.renderProviderBundleError(w, r, vmodel, i18n.T(ctx, "org.errors.provider_bundle.invalid", err.Error()))
		return
	}

	plan, err := h.planProviderBundleImport(ctx, org, p, bundle)
	if err != nil {
		slog.ErrorContext(ctx, "could not plan provider bundle import", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	vmodel.Plan = &plan
	vmodel.Bundle = string(raw)
	vmodel.Name = bundle.Provider.Name

	if r.FormValue("step") != "apply" {
		templ.Handler(component.ProviderBundleImportPage(vmodel)).ServeHTTP(w, r)
		return
	}

	sel := providerBundleSelection(r)
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
		encryptedKey, err := crypto.Encrypt(h.secretKey, apiKey)
		if err != nil {
			slog.ErrorContext(ctx, "could not encrypt API key", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		newProvider = &service.NewProviderTarget{OrgID: org.ID(), Name: name, APIKey: encryptedKey}
	}

	result, err := service.ApplyProviderBundle(ctx, h.providerStore, plan, sel, newProvider)
	if err != nil {
		if webcommon.RejectLocked(w, r, err) {
			return
		}
		slog.ErrorContext(ctx, "could not apply provider bundle", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+org.Slug()+"/admin/providers/"+string(result.Provider.ID())+"/models?success=imported", http.StatusSeeOther)
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
func (h *Handler) planProviderBundleImport(ctx context.Context, org model.Organization, p model.Provider, bundle model.ProviderBundle) (service.ProviderBundlePlan, error) {
	existing, err := h.providerStore.ListLLMModels(ctx, org.ID())
	if err != nil {
		return service.ProviderBundlePlan{}, errors.WithStack(err)
	}
	names, err := h.orgModelNames(ctx, org.ID(), existing)
	if err != nil {
		return service.ProviderBundlePlan{}, errors.WithStack(err)
	}
	return service.PlanProviderBundleImport(bundle, p, existing, &bundleProxyNames{ctx: ctx, names: names}), nil
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
	vmodel.Error = msg
	w.WriteHeader(http.StatusUnprocessableEntity)
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
