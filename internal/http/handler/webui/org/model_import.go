package org

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/bornholm/genai/llm"
	"github.com/bornholm/genai/llm/provider"
	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	xologenai "github.com/xolo-gateway/xolo/internal/adapter/genai"
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
// (CommonOptions), headers included. It returns nil when the type has no
// client for that capability.
func newProviderOptions(newOptions func(provider.Name) any, name provider.Name, baseURL, apiKey, modelName string, headers *model.ProviderRequestHeaders) any {
	opts := newOptions(name)
	if opts == nil {
		return nil
	}
	xologenai.SetCommonOptions(opts, baseURL, apiKey, modelName, headers)
	return opts
}

// discoverProviderModels lists the models the provider serves, through its
// own API type and base URL, sorted by ID.
func discoverProviderModels(ctx context.Context, providerType, baseURL, apiKey string, headers *model.ProviderRequestHeaders) ([]llm.ModelInfo, error) {
	name := provider.Name(providerType)
	opts := newProviderOptions(provider.NewDiscoveryProviderOptions, name, baseURL, apiKey, "", headers)
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
	listed := make(map[string]bool, len(discovered))
	for _, info := range discovered {
		// A model listed twice keeps its first entry, so that the table, the
		// preview and the created model all describe the same one.
		if listed[info.ID()] {
			continue
		}
		listed[info.ID()] = true
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
// serves, marked with those already declared, and the names the
// organization has already given.
type providerCatalog struct {
	candidates []component.ModelImportCandidate
	names      *orgModelNames
}

// declare records that the candidate id is now declared under proxyName.
func (c *providerCatalog) declare(id, proxyName string) {
	c.names.models[proxyName] = true
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

	discovered, err := discoverProviderModels(ctx, p.Type(), p.BaseURL(), apiKey, p.RequestHeaders())
	if err != nil {
		slog.WarnContext(ctx, "could not list provider models",
			slog.String("provider", string(p.ID())), slogx.Error(err))
		// The message helps fix a wrong URL or key. It never shows the key,
		// should an upstream echo it.
		msg := err.Error()
		if apiKey != "" {
			msg = strings.ReplaceAll(msg, apiKey, "[clé masquée]")
		}
		return nil, "Le fournisseur n'a pas pu lister ses modèles : " + msg, nil
	}

	existing, err := h.providerStore.ListLLMModels(ctx, org.ID())
	if err != nil {
		return nil, "", errors.WithStack(err)
	}
	names, err := h.orgModelNames(ctx, org.ID(), existing)
	if err != nil {
		return nil, "", errors.WithStack(err)
	}

	catalog := &providerCatalog{
		candidates: importCandidates(discovered, existing, p),
		names:      names,
	}
	return catalog, "", nil
}

// orgModelNames are the names a new model of the organization cannot take.
// They are read once per request, as a provider may list hundreds of models.
type orgModelNames struct {
	// models are the proxy names of its models, disabled ones included, as
	// the unique index on (org_id, proxy_name) counts them.
	models map[string]bool
	// virtual are the names of its virtual models. The proxy resolves those
	// first, so a model under one of them would never be served.
	virtual map[string]bool
	// teamSlugs are the slugs of its teams, which a proxy name must not
	// start with.
	teamSlugs map[string]bool
}

// orgModelNames reads the names the organization has given, existing being
// its models.
func (h *Handler) orgModelNames(ctx context.Context, orgID model.OrgID, existing []model.LLMModel) (*orgModelNames, error) {
	virtual, err := h.orgVirtualModelNames(ctx, orgID)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	teamSlugs, err := h.orgTeamSlugs(ctx, orgID)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return &orgModelNames{models: takenProxyNames(existing), virtual: virtual, teamSlugs: teamSlugs}, nil
}

func takenProxyNames(existing []model.LLMModel) map[string]bool {
	taken := make(map[string]bool, len(existing))
	for _, m := range existing {
		taken[m.ProxyName()] = true
	}
	return taken
}

// conflict explains why proxyName cannot name a new model of the
// organization. It is empty when the name is free. The checks are those of
// the model form, made on sets read once.
func (n *orgModelNames) conflict(proxyName string) string {
	if n.models[proxyName] {
		return "Un modèle s'appelle déjà « " + proxyName + " » dans cette organisation."
	}
	if n.virtual[proxyName] {
		return virtualModelConflictMessage(proxyName)
	}
	// Mirrors service.CheckModelLocalName.
	if segment, _, found := strings.Cut(proxyName, "/"); found && n.teamSlugs[segment] {
		return teamNameConflictMessage(proxyName)
	}
	return ""
}

// nameCandidates gives each candidate not yet declared the proxy name it
// would be imported under with prefix, and why that name cannot be used, so
// that the page shows a conflict before the import refuses it.
func nameCandidates(candidates []component.ModelImportCandidate, names *orgModelNames, prefix string) {
	for i := range candidates {
		c := &candidates[i]
		if c.DeclaredAs != "" {
			c.ProxyName, c.Conflict = "", ""
			continue
		}
		c.ProxyName = prefix + c.ID
		c.Conflict = names.conflict(c.ProxyName)
	}
}

// prefixError checks the proxy prefix. It returns the message for the page,
// or an empty string.
func prefixError(prefix string) string {
	if strings.Contains(prefix, "/") {
		// A slash would read as an organization or team segment of the name.
		return "Le préfixe ne peut pas contenir « / »."
	}
	return ""
}

// importFormError checks the import form before the provider is asked
// anything. It returns the message for the page, or an empty string.
func importFormError(prefix string, selected []string) string {
	if msg := prefixError(prefix); msg != "" {
		return msg
	}
	if len(selected) == 0 {
		return "Cochez au moins un modèle à importer."
	}
	return ""
}

// uniqueValues returns values without repeats, in their first order.
func uniqueValues(values []string) []string {
	seen := make(map[string]bool, len(values))
	unique := make([]string, 0, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			unique = append(unique, v)
		}
	}
	return unique
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
		templ.Handler(component.ModelImportPage(vmodel)).ServeHTTP(w, r)
		return
	}
	nameCandidates(catalog.candidates, catalog.names, "")
	vmodel.Candidates = catalog.candidates
	templ.Handler(component.ModelImportPage(vmodel)).ServeHTTP(w, r)
}

// previewImportNames renders the proxy names the listed candidates would get
// with the prefix being typed, as out-of-band swaps of their table cells. It
// reads the organization's models only: the provider is not asked again on
// each keystroke.
func (h *Handler) previewImportNames(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	org, _, err := h.resolveOrgAndProvider(ctx, r.PathValue("orgSlug"), r.PathValue("providerID"))
	if err != nil {
		writeResourceLookupError(ctx, w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	// The form lists every row of the table in order, an empty value for a
	// model already declared, so that each index names its row.
	ids := r.Form["candidate"]
	candidates := make([]component.ModelImportCandidate, len(ids))
	for i, id := range ids {
		candidates[i].ID = id
		if id == "" {
			// Its declared name does not matter here: the row has nothing to
			// preview.
			candidates[i].DeclaredAs = "-"
		}
	}

	existing, err := h.providerStore.ListLLMModels(ctx, org.ID())
	if err != nil {
		slog.ErrorContext(ctx, "could not preview import names", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	names, err := h.orgModelNames(ctx, org.ID(), existing)
	if err != nil {
		slog.ErrorContext(ctx, "could not preview import names", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	prefix := strings.TrimSpace(r.FormValue("proxy_prefix"))
	nameCandidates(candidates, names, prefix)
	// The preview applies the import's own rule on the prefix, so that it
	// never announces names the import would refuse.
	templ.Handler(component.ModelImportProxyNames(candidates, prefixError(prefix))).ServeHTTP(w, r)
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
	selected := uniqueValues(r.Form["models"])
	formErr := importFormError(prefix, selected)

	// The list is fetched again rather than trusted from the form: only
	// models the provider serves right now are imported, with the limits it
	// reports. An invalid form still needs it, to show the table again.
	catalog, discoveryErr, err := h.loadProviderCatalog(ctx, org, p)
	if err != nil {
		slog.ErrorContext(ctx, "could not prepare model import", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	if catalog != nil {
		// A model left out keeps its box checked, for the next attempt.
		for i := range catalog.candidates {
			catalog.candidates[i].Checked = slices.Contains(selected, catalog.candidates[i].ID)
		}
	}

	vmodel := h.modelImportVModel(ctx, org, p)
	vmodel.Prefix = prefix
	if formErr != "" {
		// The form's own mistake comes first: a provider failing at the same
		// time must not hide it. The table then has no list to show, and the
		// page answers like a failed discovery, with the form's message only.
		vmodel.Error = formErr
		if discoveryErr == "" {
			nameCandidates(catalog.candidates, catalog.names, prefix)
			vmodel.Candidates = catalog.candidates
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		templ.Handler(component.ModelImportPage(vmodel)).ServeHTTP(w, r)
		return
	}
	if discoveryErr != "" {
		vmodel.Error = discoveryErr
		w.WriteHeader(http.StatusBadGateway)
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
	nameCandidates(catalog.candidates, catalog.names, prefix)
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
			break
		}
	}
	if c == nil {
		return id + " : le fournisseur ne le liste plus.", nil
	}
	if c.DeclaredAs != "" {
		return id + " : déjà déclaré sous le nom « " + c.DeclaredAs + " ».", nil
	}

	proxyName := prefix + id
	if conflict := catalog.names.conflict(proxyName); conflict != "" {
		return id + " : " + conflict, nil
	}

	// Costs stay at zero: the provider's list carries no price, and a price
	// guessed elsewhere would be billed as if it were known. The model starts
	// disabled, so that it is not served unpriced before an admin fills it.
	m := model.NewLLMModel(p.ID(), org.ID(), proxyName, id, c.DisplayName, 0, 0)
	m.SetEnabled(false)
	m.SetContextWindow(c.ContextWindow)
	m.SetOutputWindow(c.OutputWindow)
	if err := h.providerStore.CreateLLMModel(ctx, m); err != nil {
		slog.ErrorContext(ctx, "could not create imported model", slog.String("model", id), slogx.Error(err))
		// Another admin may have taken the name since the catalog was read:
		// retrying would then fail the same way. The models are listed rather
		// than looked up by name, as that lookup skips disabled ones.
		existing, lookupErr := h.providerStore.ListLLMModels(ctx, org.ID())
		if lookupErr != nil {
			// The cause stays unknown: a retry is not promised to succeed.
			slog.ErrorContext(ctx, "could not check imported model name", slog.String("model", id), slogx.Error(lookupErr))
			return id + " : l'enregistrement a échoué, et Xolo n'a pas pu vérifier si le nom « " + proxyName + " » est déjà pris.", nil
		}
		if takenProxyNames(existing)[proxyName] {
			return id + " : Un modèle s'appelle déjà « " + proxyName + " » dans cette organisation.", nil
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
