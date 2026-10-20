package org

import (
	"context"
	"io"
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
	defer closeClient(client)

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

// providerCatalog is what an import works from: the models the provider
// serves, marked with those already declared, and the proxy names taken in
// the organization, disabled models included, as the unique index on
// (org_id, proxy_name) counts them.
type providerCatalog struct {
	candidates []component.ModelImportCandidate
	taken      map[string]bool
}

// declare records that the candidate id is now declared under proxyName.
func (c *providerCatalog) declare(id, proxyName string) {
	c.taken[proxyName] = true
	for i := range c.candidates {
		if c.candidates[i].ID == id {
			c.candidates[i].DeclaredAs = proxyName
		}
	}
}

// loadProviderCatalog discovers the provider's models and reads the models
// of the organization. A discovery failure is returned as a message for the
// page, a store failure as an error.
func (h *Handler) loadProviderCatalog(ctx context.Context, org model.Organization, p model.Provider) (*providerCatalog, string, error) {
	apiKey, err := crypto.Decrypt(h.secretKey, p.APIKey())
	if err != nil {
		return nil, "", errors.Wrap(err, "could not decrypt provider API key")
	}

	discovered, err := discoverProviderModels(ctx, p.Type(), p.BaseURL(), apiKey)
	if err != nil {
		slog.WarnContext(ctx, "could not list provider models",
			slog.String("provider", string(p.ID())), slogx.Error(err))
		// The message helps fix a wrong URL or key. It never shows the key,
		// should an upstream echo it.
		return nil, "Le fournisseur n'a pas pu lister ses modèles : " + strings.ReplaceAll(err.Error(), apiKey, "[clé masquée]"), nil
	}

	existing, err := h.providerStore.ListLLMModels(ctx, org.ID())
	if err != nil {
		return nil, "", errors.WithStack(err)
	}

	catalog := &providerCatalog{
		candidates: importCandidates(discovered, existing, p),
		taken:      make(map[string]bool, len(existing)),
	}
	for _, m := range existing {
		catalog.taken[m.ProxyName()] = true
	}
	return catalog, "", nil
}

func (h *Handler) getImportModelsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgSlug := r.PathValue("orgSlug")

	org, p, err := h.resolveOrgAndProvider(ctx, orgSlug, r.PathValue("providerID"))
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}

	catalog, discoveryErr, err := h.loadProviderCatalog(ctx, org, p)
	if err != nil {
		slog.ErrorContext(ctx, "could not prepare model import", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	vmodel := h.modelImportVModel(ctx, org, p)
	vmodel.Error = discoveryErr
	if discoveryErr != "" {
		w.WriteHeader(http.StatusBadGateway)
	} else {
		vmodel.Candidates = catalog.candidates
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
	catalog, discoveryErr, err := h.loadProviderCatalog(ctx, org, p)
	if err != nil {
		slog.ErrorContext(ctx, "could not prepare model import", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	vmodel := h.modelImportVModel(ctx, org, p)
	vmodel.Prefix = prefix
	if discoveryErr != "" {
		vmodel.Error = discoveryErr
		w.WriteHeader(http.StatusBadGateway)
		templ.Handler(component.ModelImportPage(vmodel)).ServeHTTP(w, r)
		return
	}
	vmodel.Candidates = catalog.candidates
	switch {
	case strings.Contains(prefix, "/"):
		// A slash would read as an organization or team segment of the name.
		vmodel.Error = "Le préfixe ne peut pas contenir « / »."
	case len(selected) == 0:
		vmodel.Error = "Cochez au moins un modèle à importer."
	}
	if vmodel.Error != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
		templ.Handler(component.ModelImportPage(vmodel)).ServeHTTP(w, r)
		return
	}

	imported := 0
	for _, id := range selected {
		skip, err := h.importModel(ctx, org, p, catalog, id, prefix)
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

	// Some models were left out: show why, on the list updated with the ones
	// just created, without asking the provider again.
	vmodel.Imported = imported
	vmodel.Candidates = catalog.candidates
	templ.Handler(component.ModelImportPage(vmodel)).ServeHTTP(w, r)
}

// importModel creates the model id under the given proxy prefix and records
// it in the catalog. It returns why the model was left out, or an empty
// string once created. A model the store fails to create is left out too, so
// that the report says which models of the selection did get through.
func (h *Handler) importModel(ctx context.Context, org model.Organization, p model.Provider, catalog *providerCatalog, id, prefix string) (string, error) {
	var c *component.ModelImportCandidate
	for i := range catalog.candidates {
		if catalog.candidates[i].ID == id {
			c = &catalog.candidates[i]
		}
	}
	if c == nil {
		return id + " : le fournisseur ne le liste plus.", nil
	}
	if c.DeclaredAs != "" {
		return id + " : déjà déclaré sous le nom « " + c.DeclaredAs + " ».", nil
	}

	proxyName := prefix + id
	if catalog.taken[proxyName] {
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
		slog.ErrorContext(ctx, "could not create imported model", slog.String("model", id), slogx.Error(err))
		// Another admin may have taken the name since the catalog was read:
		// retrying would then fail the same way.
		if existing, lookupErr := h.providerStore.GetLLMModelByProxyName(ctx, org.ID(), proxyName); lookupErr == nil && existing != nil {
			return id + " : un modèle s'appelle déjà « " + proxyName + " » dans cette organisation.", nil
		}
		return id + " : l'enregistrement a échoué, réessayez.", nil
	}
	catalog.declare(id, proxyName)
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

// closeClient releases a client built for a single call: a plugin client
// holds a process.
func closeClient(client any) {
	if c, ok := client.(io.Closer); ok {
		_ = c.Close()
	}
}
