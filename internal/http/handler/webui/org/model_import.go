package org

import (
	"context"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/genai/llm"
	"github.com/bornholm/genai/llm/provider"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/crypto"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
)

// modelDiscoveryTimeout bounds the call to the provider's model list, so a
// silent upstream does not hold the admin page.
const modelDiscoveryTimeout = 30 * time.Second

// newProviderOptions returns the options of the given provider type for one
// capability, filled with the connection settings shared by every provider
// (CommonOptions). It returns nil when the type has no client for that
// capability.
func newProviderOptions(newOptions func(provider.Name) any, name provider.Name, baseURL, apiKey, modelName string) any {
	opts := newOptions(name)
	if opts == nil {
		return nil
	}
	v := reflect.ValueOf(opts).Elem()
	if common := v.FieldByName("CommonOptions"); common.IsValid() {
		common.FieldByName("BaseURL").SetString(baseURL)
		common.FieldByName("APIKey").SetString(apiKey)
		common.FieldByName("Model").SetString(modelName)
	}
	return opts
}

// discoverProviderModels lists the models the provider serves, through its
// own API type and base URL, sorted by ID.
func discoverProviderModels(ctx context.Context, providerType, baseURL, apiKey string) ([]llm.ModelInfo, error) {
	name := provider.Name(providerType)
	opts := newProviderOptions(provider.NewDiscoveryProviderOptions, name, baseURL, apiKey, "")
	if opts == nil {
		return nil, errors.Errorf("le type de fournisseur %q ne sait pas lister ses modèles", providerType)
	}

	client, err := provider.Create(ctx, func(o *provider.Options) error {
		o.Discovery = &provider.ResolvedClientOptions{Provider: name, Specific: opts}
		return nil
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	discovery, ok := client.(llm.DiscoveryClient)
	if !ok {
		return nil, errors.Errorf("le type de fournisseur %q ne sait pas lister ses modèles", providerType)
	}

	ctx, cancel := context.WithTimeout(ctx, modelDiscoveryTimeout)
	defer cancel()

	models, err := discovery.Models(ctx)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	sort.Slice(models, func(i, j int) bool { return models[i].ID() < models[j].ID() })
	return models, nil
}

// importCandidates pairs each discovered model with the model of the
// provider already serving it, if any.
func importCandidates(discovered []llm.ModelInfo, existing []model.LLMModel, p model.Provider) []component.ModelImportCandidate {
	declared := map[string]string{}
	for _, m := range existing {
		if m.ProviderID() == p.ID() {
			declared[m.RealModel()] = m.ProxyName()
		}
	}

	candidates := make([]component.ModelImportCandidate, 0, len(discovered))
	for _, info := range discovered {
		candidates = append(candidates, component.ModelImportCandidate{
			ID:            info.ID(),
			DisplayName:   info.DisplayName(),
			ContextWindow: info.ContextWindow(),
			OutputWindow:  info.MaxOutputTokens(),
			DeclaredAs:    declared[info.ID()],
		})
	}
	return candidates
}

// listProviderCandidates discovers the provider's models and marks those
// already declared. A discovery failure is returned as a message for the
// page, a store failure as an error.
func (h *Handler) listProviderCandidates(ctx context.Context, org model.Organization, p model.Provider) ([]component.ModelImportCandidate, string, error) {
	apiKey, err := crypto.Decrypt(h.secretKey, p.APIKey())
	if err != nil {
		return nil, "", errors.Wrap(err, "could not decrypt provider API key")
	}

	discovered, err := discoverProviderModels(ctx, p.Type(), p.BaseURL(), apiKey)
	if err != nil {
		slog.WarnContext(ctx, "could not list provider models",
			slog.String("provider", string(p.ID())), slogx.Error(err))
		return nil, "Le fournisseur n'a pas pu lister ses modèles : " + err.Error(), nil
	}

	existing, err := h.providerStore.ListLLMModels(ctx, org.ID())
	if err != nil {
		return nil, "", errors.WithStack(err)
	}

	return importCandidates(discovered, existing, p), "", nil
}

func (h *Handler) getImportModelsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")

	org, p, err := h.resolveOrgAndProvider(ctx, orgSlug, r.PathValue("providerID"))
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	candidates, discoveryErr, err := h.listProviderCandidates(ctx, org, p)
	if err != nil {
		slog.ErrorContext(ctx, "could not prepare model import", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	vmodel := h.modelImportVModel(ctx, org, p)
	vmodel.Candidates = candidates
	vmodel.Error = discoveryErr
	if discoveryErr != "" {
		w.WriteHeader(http.StatusBadGateway)
	}
	templ.Handler(component.ModelImportPage(vmodel)).ServeHTTP(w, r)
}

func (h *Handler) importModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")

	org, p, err := h.resolveOrgAndProvider(ctx, orgSlug, r.PathValue("providerID"))
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	prefix := strings.TrimSpace(r.FormValue("proxy_prefix"))
	selected := r.Form["models"]

	// The list is fetched again rather than trusted from the form: only
	// models the provider serves right now are imported, with the limits it
	// reports.
	candidates, discoveryErr, err := h.listProviderCandidates(ctx, org, p)
	if err != nil {
		slog.ErrorContext(ctx, "could not prepare model import", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	vmodel := h.modelImportVModel(ctx, org, p)
	vmodel.Candidates = candidates
	vmodel.Prefix = prefix
	if discoveryErr != "" {
		vmodel.Error = discoveryErr
		w.WriteHeader(http.StatusBadGateway)
		templ.Handler(component.ModelImportPage(vmodel)).ServeHTTP(w, r)
		return
	}
	if len(selected) == 0 {
		vmodel.Error = "Cochez au moins un modèle à importer."
		w.WriteHeader(http.StatusUnprocessableEntity)
		templ.Handler(component.ModelImportPage(vmodel)).ServeHTTP(w, r)
		return
	}

	byID := make(map[string]component.ModelImportCandidate, len(candidates))
	for _, c := range candidates {
		byID[c.ID] = c
	}

	imported := 0
	for _, id := range selected {
		skip, err := h.importModel(ctx, org, p, byID, id, prefix)
		if err != nil {
			slog.ErrorContext(ctx, "could not import model", slog.String("model", id), slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if skip != "" {
			vmodel.Skipped = append(vmodel.Skipped, skip)
			continue
		}
		imported++
	}

	if len(vmodel.Skipped) == 0 {
		http.Redirect(w, r, "/orgs/"+orgSlug+"/admin/providers/"+string(p.ID())+"/models?success=imported", http.StatusSeeOther)
		return
	}

	// Some models were left out: show why, on a list refreshed with the
	// ones just created.
	vmodel.Imported = imported
	vmodel.Candidates, _, err = h.listProviderCandidates(ctx, org, p)
	if err != nil {
		slog.ErrorContext(ctx, "could not refresh model import", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	templ.Handler(component.ModelImportPage(vmodel)).ServeHTTP(w, r)
}

// importModel creates the model id under the given proxy prefix. It returns
// why the model was left out, or an empty string once created.
func (h *Handler) importModel(ctx context.Context, org model.Organization, p model.Provider, candidates map[string]component.ModelImportCandidate, id, prefix string) (string, error) {
	c, ok := candidates[id]
	if !ok {
		return id + " : le fournisseur ne le liste plus.", nil
	}
	if c.DeclaredAs != "" {
		return id + " : déjà déclaré sous le nom « " + c.DeclaredAs + " ».", nil
	}

	proxyName := prefix + id
	if conflicting, err := h.providerStore.GetLLMModelByProxyName(ctx, org.ID(), proxyName); err == nil && conflicting != nil {
		return id + " : un modèle s'appelle déjà « " + proxyName + " » dans cette organisation.", nil
	}
	msg, err := h.teamNameConflict(ctx, org.ID(), proxyName)
	if err != nil {
		return "", errors.WithStack(err)
	}
	if msg != "" {
		return id + " : " + msg, nil
	}

	// Costs stay at zero: the provider's list carries no price, and a price
	// guessed elsewhere would be billed as if it were known.
	m := model.NewLLMModel(p.ID(), org.ID(), proxyName, id, c.DisplayName, 0, 0)
	m.SetContextWindow(c.ContextWindow)
	m.SetOutputWindow(c.OutputWindow)
	if err := h.providerStore.CreateLLMModel(ctx, m); err != nil {
		return "", errors.WithStack(err)
	}
	return "", nil
}

func (h *Handler) modelImportVModel(ctx context.Context, org model.Organization, p model.Provider) component.ModelImportVModel {
	orgSlug := org.Slug()
	return component.ModelImportVModel{
		Org:      org,
		Provider: p,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         httpCtx.User(ctx),
			SelectedItem: "org-" + orgSlug + "-providers",
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  orgSlug,
			ContextOrgID: org.ID(),
			Breadcrumbs: []common.BreadcrumbItem{
				{Label: org.Name(), Href: "/orgs/" + orgSlug + "/usage"},
				{Label: "Fournisseurs", Href: "/orgs/" + orgSlug + "/admin/providers"},
				{Label: p.Name(), Href: "/orgs/" + orgSlug + "/admin/providers/" + string(p.ID()) + "/models"},
				{Label: "Importer", Href: ""},
			},
		},
	}
}
