package service

import (
	"context"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/bornholm/go-x/slogx"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/catalog"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// catalogFollowedFields are the model fields the catalog gives, which
// following it keeps up to date. The proxy name, the enabled flag, the token
// limit and the extra body are the operator's alone.
var catalogFollowedFields = []string{
	BundleFieldDescription,
	BundleFieldKind,
	BundleFieldPrices,
	BundleFieldWindows,
	BundleFieldActiveParams,
	BundleFieldThroughput,
	BundleFieldCapabilities,
	BundleFieldAPI,
}

func bundleField(name string) bundleModelField {
	for _, f := range bundleModelFields {
		if f.name == name {
			return f
		}
	}
	panic("service: unknown bundle field " + name)
}

func sameField(f bundleModelField, a, b model.ProviderBundleModel) bool {
	return reflect.DeepEqual(f.get(a), f.get(b))
}

// CatalogImport is the catalog entry the bundle of an import was converted
// from: the catalog import posts it back with the bundle, so that the models
// the import writes remember the release they come from.
type CatalogImport struct {
	// Provider is the catalog provider, Version the release.
	Provider string `json:"provider"`
	Version  string `json:"version"`
	// Currency is the currency of Models' prices, the catalog provider's.
	Currency string `json:"currency"`
	// Models are the bundle's models as the release gave them, before any
	// currency conversion.
	Models []model.ProviderBundleModel `json:"models"`
	// Estimated names, per RealModel, the model form fields the catalog
	// estimates.
	Estimated map[string][]string `json:"estimated,omitempty"`
}

// NewCatalogImport describes the catalog provider catalogProvider of the
// release version, converted into tb.
func NewCatalogImport(catalogProvider, version string, tb catalog.TargetBundle) CatalogImport {
	return CatalogImport{
		Provider:  catalogProvider,
		Version:   version,
		Currency:  tb.Bundle.Provider.Currency,
		Models:    slices.Clone(tb.Bundle.Models),
		Estimated: tb.Estimated,
	}
}

// Origin is the origin of a model imported with the values applied, in the
// provider's currency, of the catalog model raw.
func (ci CatalogImport) Origin(raw, applied model.ProviderBundleModel) model.CatalogOrigin {
	return model.CatalogOrigin{
		Provider:  ci.Provider,
		Model:     raw.RealModel,
		Version:   ci.Version,
		Currency:  ci.Currency,
		Catalog:   raw,
		Applied:   applied,
		Estimated: ci.Estimated[raw.RealModel],
	}
}

// RecordCatalogOrigins records the origin of the models an import of ci
// wrote, as planned by plan and applied with sel into result: the models it
// created, the ones it updated, and the ones it found unchanged. A model it
// failed to write, or the operator left out, gets none.
func RecordCatalogOrigins(ctx context.Context, store port.ProviderStore, origins port.CatalogOriginStore, ci CatalogImport, plan ProviderBundlePlan, sel BundleSelection, result BundleApplyResult) error {
	if result.Provider == nil {
		return nil
	}
	models, err := store.ListLLMModels(ctx, result.Provider.OrgID())
	if err != nil {
		return errors.WithStack(err)
	}
	failed := map[int]bool{}
	for _, f := range result.Failed {
		failed[f.Index] = true
	}
	for _, mp := range plan.Models {
		i := slices.IndexFunc(ci.Models, func(bm model.ProviderBundleModel) bool { return bm.RealModel == mp.Model.RealModel })
		if i < 0 || failed[mp.Index] {
			continue
		}
		var id model.LLMModelID
		switch mp.Action {
		case BundleModelCreate:
			if !sel.Models[mp.Index] {
				continue
			}
			j := slices.IndexFunc(models, func(m model.LLMModel) bool {
				return m.ProviderID() == result.Provider.ID() && m.ProxyName() == mp.Model.ProxyName && m.RealModel() == mp.Model.RealModel
			})
			if j < 0 {
				continue
			}
			id = models[j].ID()
		case BundleModelUpdate:
			if len(sel.Fields[mp.Index]) == 0 {
				continue
			}
			id = mp.Existing.ID()
		case BundleModelUnchanged:
			id = mp.Existing.ID()
		default:
			continue
		}
		origin := ci.Origin(ci.Models[i], mp.Model)
		if err := origins.SaveCatalogOrigin(ctx, id, &origin); err != nil {
			return errors.WithStack(err)
		}
	}
	return nil
}

// CatalogDivergence is a field of a model that differs from its catalog
// value: the operator changed it, or the catalog changed it and the model
// could not take it. Catalog is the catalog's value, in the provider's
// currency.
type CatalogDivergence struct {
	Field   string
	Current string
	Catalog string
}

// CatalogDivergences lists the fields of m that differ from the catalog
// values recorded in origin.
func CatalogDivergences(m model.LLMModel, origin model.CatalogOrigin) []CatalogDivergence {
	current := model.BundleModelOf(m)
	var divergences []CatalogDivergence
	for _, name := range catalogFollowedFields {
		f := bundleField(name)
		if sameField(f, current, origin.Applied) {
			continue
		}
		divergences = append(divergences, CatalogDivergence{Field: name, Current: displayValue(f.get(current)), Catalog: displayValue(f.get(origin.Applied))})
	}
	return divergences
}

// CatalogEstimated reports whether the model form field formField of m holds
// the catalog's estimate: the catalog estimates it, and the operator did not
// change the field it belongs to.
func CatalogEstimated(m model.LLMModel, origin model.CatalogOrigin, formField string) bool {
	if !slices.Contains(origin.Estimated, formField) {
		return false
	}
	name, ok := formFieldBundleField[formField]
	if !ok {
		return false
	}
	return sameField(bundleField(name), model.BundleModelOf(m), origin.Applied)
}

// formFieldBundleField maps the model form fields the catalog may estimate
// to the bundle field holding them.
var formFieldBundleField = map[string]string{
	"prompt_cost":         BundleFieldPrices,
	"cached_prompt_cost":  BundleFieldPrices,
	"completion_cost":     BundleFieldPrices,
	"context_window":      BundleFieldWindows,
	"output_window":       BundleFieldWindows,
	"active_params":       BundleFieldActiveParams,
	"tokens_per_sec_low":  BundleFieldThroughput,
	"tokens_per_sec_high": BundleFieldThroughput,
}

// AcceptCatalogValue writes the catalog value of field to the model m, which
// has the origin origin, through the bundle import: the same checks apply
// as to an import, the kind served by the API among them. It returns
// ErrInvalid when the field is not one the catalog gives, or when the
// import refuses it.
func AcceptCatalogValue(ctx context.Context, store port.ProviderStore, p model.Provider, m model.LLMModel, origin model.CatalogOrigin, field string) error {
	if !slices.Contains(catalogFollowedFields, field) {
		return errors.Wrapf(port.ErrInvalid, "%q is no field of the catalog", field)
	}
	result, err := applyCatalogFields(ctx, store, p, []catalogFieldWrite{{model: m, values: origin.Applied, fields: []string{field}}})
	if err != nil {
		return err
	}
	if len(result.Failed) > 0 {
		return result.Failed[0].Err
	}
	if len(result.Skipped) > 0 {
		return errors.Wrapf(port.ErrInvalid, "the catalog value of %q cannot be applied", field)
	}
	return nil
}

// catalogFieldWrite is the catalog values of fields to write to model.
type catalogFieldWrite struct {
	model  model.LLMModel
	values model.ProviderBundleModel
	fields []string
}

// applyCatalogFields writes the given fields of each model of p through the
// bundle import, so that the projections, the audit and the event feed see
// them and the import's checks apply. The models keep their names: no write
// renames one.
func applyCatalogFields(ctx context.Context, store port.ProviderStore, p model.Provider, writes []catalogFieldWrite) (BundleApplyResult, error) {
	models, err := store.ListLLMModels(ctx, p.OrgID())
	if err != nil {
		return BundleApplyResult{}, errors.WithStack(err)
	}
	// The provider's own settings: the plan offers no change of them.
	bundle := model.NewProviderBundle(p, nil, false, time.Now())
	sel := BundleSelection{Fields: map[int]map[string]bool{}}
	for _, w := range writes {
		bm := model.BundleModelOf(w.model)
		fields := map[string]bool{}
		for _, name := range w.fields {
			bundleField(name).set(&bm, w.values)
			fields[name] = true
		}
		sel.Fields[len(bundle.Models)] = fields
		bundle.Models = append(bundle.Models, bm)
	}
	plan := PlanProviderBundleImportWith(bundle, p, models, keptNames{}, &sel)
	return ApplyProviderBundle(ctx, store, plan, sel, nil)
}

// keptNames refuses every name: following the catalog renames no model, and
// creates none.
type keptNames struct{}

func (keptNames) Conflict(string) string { return "following the catalog renames no model" }
func (keptNames) Declare(string)         {}
func (keptNames) Release(string)         {}

// CatalogFollower keeps the models imported from the open model catalog up
// to date with its releases: a value a newer release changes is written to
// the models that still hold the previous one, while a value the operator
// changed is kept, and offered in the model form.
type CatalogFollower struct {
	providers port.ProviderStore
	origins   port.CatalogOriginStore
	rates     CurrencyConverter
	// mu runs one follow at a time.
	mu sync.Mutex
}

// NewCatalogFollower follows the catalog for the models of providers, whose
// origins are in origins. rates converts the catalog prices into the
// currency of a provider billing in another; nil leaves such providers as
// they are.
func NewCatalogFollower(providers port.ProviderStore, origins port.CatalogOriginStore, rates CurrencyConverter) *CatalogFollower {
	return &CatalogFollower{providers: providers, origins: origins, rates: rates}
}

// CatalogFollowReport counts what following a release did.
type CatalogFollowReport struct {
	// Models counts the models compared with the release.
	Models int
	// Updated counts the models a catalog change was written to, Fields the
	// fields written.
	Updated int
	Fields  int
	// Kept counts the catalog changes left out because the operator changed
	// the field, or because the import refused them.
	Kept int
	// Failed counts the models the store refused to write: they are
	// compared with the release again next time.
	Failed int
}

// Follow compares the models imported from an older release than c with c.
// For each field the catalog gives, a model whose value is still the one it
// was imported with takes the new catalog value; one the operator changed
// keeps its own. A model whose provider or offering the release no longer
// has is left as it is. Following is idempotent: it can run on every
// replica, and again on the same release.
//
// It reads the models before writing them: an edit saved in between is
// overwritten by the bundle import's write of the whole model.
func (f *CatalogFollower) Follow(ctx context.Context, c *catalog.Catalog) (CatalogFollowReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var report CatalogFollowReport
	tracked, err := f.origins.ListCatalogTrackedModels(ctx)
	if err != nil {
		return report, errors.WithStack(err)
	}
	// The models to follow, per provider and catalog provider, in order.
	type group struct {
		provider        model.ProviderID
		catalogProvider string
	}
	var order []group
	groups := map[group][]model.CatalogTrackedModel{}
	for _, t := range tracked {
		if t.Origin.Version == c.Version {
			continue
		}
		g := group{t.ProviderID, t.Origin.Provider}
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], t)
	}
	for _, g := range order {
		if err := f.followGroup(ctx, c, g.provider, g.catalogProvider, groups[g], &report); err != nil {
			// One provider's failure leaves the others to follow.
			slog.WarnContext(ctx, "could not follow the model catalog", slog.String("provider", string(g.provider)), slog.String("catalog_provider", g.catalogProvider), slogx.Error(err))
		}
	}
	return report, nil
}

// Run follows the releases source serves until ctx is done: it fetches the
// release every interval, a minute at least, and follows it. While no release
// can be read, it tries again after a minute.
func (f *CatalogFollower) Run(ctx context.Context, source *catalog.Source, interval time.Duration) {
	interval = max(interval, time.Minute)
	for {
		source.Refresh(ctx)
		wait := interval
		c, err := source.Catalog(ctx)
		if err != nil {
			wait = time.Minute
		} else if report, err := f.Follow(ctx, c); err != nil {
			slog.WarnContext(ctx, "could not follow the model catalog", slog.String("version", c.Version), slogx.Error(err))
		} else if report.Updated > 0 || report.Kept > 0 || report.Failed > 0 {
			slog.InfoContext(ctx, "followed the model catalog", slog.String("version", c.Version),
				slog.Int("models", report.Models), slog.Int("updated", report.Updated), slog.Int("fields", report.Fields),
				slog.Int("kept", report.Kept), slog.Int("failed", report.Failed))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (f *CatalogFollower) followGroup(ctx context.Context, c *catalog.Catalog, providerID model.ProviderID, catalogProvider string, tracked []model.CatalogTrackedModel, report *CatalogFollowReport) error {
	p, err := f.providers.GetProviderByID(ctx, providerID)
	if errors.Is(err, port.ErrNotFound) {
		return nil
	}
	if err != nil {
		return errors.WithStack(err)
	}
	tb, err := c.BundleFor(catalogProvider, "", p)
	if errors.Is(err, catalog.ErrNotFound) {
		// The release no longer has the provider: its models keep their
		// values, and their origin, should it come back.
		return nil
	}
	if err != nil {
		return errors.WithStack(err)
	}
	converted := tb.Bundle
	converted.Models = slices.Clone(tb.Bundle.Models)
	if p.Currency() != converted.Provider.Currency {
		if f.rates == nil || p.Currency() == "" {
			return errors.Errorf("cannot convert %s prices into %q", converted.Provider.Currency, p.Currency())
		}
		// Converted at today's rate. A price the catalog did not change is
		// not written again: the model keeps the one it was imported with.
		if err := ConvertBundleCurrency(ctx, f.rates, &converted, p.Currency()); err != nil {
			return errors.Wrap(err, "could not convert the catalog prices")
		}
	}
	models, err := f.providers.ListLLMModels(ctx, p.OrgID())
	if err != nil {
		return errors.WithStack(err)
	}
	byID := make(map[model.LLMModelID]model.LLMModel, len(models))
	for _, m := range models {
		if m.ProviderID() == p.ID() {
			byID[m.ID()] = m
		}
	}

	var writes []catalogFieldWrite
	next := map[model.LLMModelID]model.CatalogOrigin{}
	for _, t := range tracked {
		m, ok := byID[t.ModelID]
		if !ok {
			continue
		}
		i := slices.IndexFunc(tb.Bundle.Models, func(bm model.ProviderBundleModel) bool { return bm.RealModel == t.Origin.Model })
		if i < 0 {
			// The offering is gone, or the provider's endpoint no longer
			// serves it: the model keeps its values.
			continue
		}
		report.Models++
		raw, applied := tb.Bundle.Models[i], converted.Models[i]
		current := model.BundleModelOf(m)
		origin := t.Origin
		origin.Version, origin.Currency, origin.Catalog, origin.Estimated = c.Version, tb.Bundle.Provider.Currency, raw, tb.Estimated[raw.RealModel]
		var take []string
		for _, name := range catalogFollowedFields {
			field := bundleField(name)
			if sameField(field, raw, t.Origin.Catalog) {
				continue
			}
			// The catalog changed the field: the model takes it when it
			// still holds the value it was imported with.
			if sameField(field, current, t.Origin.Applied) {
				take = append(take, name)
			} else {
				report.Kept++
			}
			field.set(&origin.Applied, applied)
		}
		next[m.ID()] = origin
		if len(take) > 0 {
			writes = append(writes, catalogFieldWrite{model: m, values: applied, fields: take})
		}
	}

	if len(writes) > 0 {
		result, err := applyCatalogFields(ctx, f.providers, p, writes)
		if err != nil {
			return err
		}
		for _, failure := range result.Failed {
			// The model still holds its values: compared again next time.
			delete(next, failure.ModelID)
			report.Failed++
			slog.WarnContext(ctx, "could not follow the model catalog for a model", slog.String("model", failure.ProxyName), slogx.Error(failure.Err))
		}
		report.Kept += len(result.Skipped)
		for _, w := range writes {
			if _, ok := next[w.model.ID()]; !ok {
				continue
			}
			refused := 0
			for _, s := range result.Skipped {
				if s.Existing != nil && s.Existing.ID() == w.model.ID() {
					refused++
				}
			}
			if written := len(w.fields) - refused; written > 0 {
				report.Updated++
				report.Fields += written
			}
		}
	}
	for _, t := range tracked {
		origin, ok := next[t.ModelID]
		if !ok {
			continue
		}
		if err := f.origins.SaveCatalogOrigin(ctx, t.ModelID, &origin); err != nil && !errors.Is(err, port.ErrNotFound) {
			return errors.WithStack(err)
		}
	}
	return nil
}
