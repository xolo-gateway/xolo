package service

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/secretcleanup"
	"github.com/xolo-gateway/xolo/internal/crypto"
)

const (
	maxNodeSecretKeyLength = 200
	maxNodeSecrets         = 50
)

// virtualModelWriter and middlewareWriter are what the stores return and
// the constructors build: entities whose every field is writable.
type virtualModelWriter interface {
	model.VirtualModel
	model.PipelineEntity
	SetName(string)
	SetCatalogOverrides(*model.CatalogOverrides)
}

type middlewareWriter interface {
	model.Middleware
	SetName(string)
	SetEnabled(bool)
	SetPriority(int)
	SetAppliesToAll(bool)
	SetTargets([]model.ModelRef)
}

// PutVirtualModel creates the virtual model or brings it to the given
// representation. The secrets of the plugin nodes it removes are removed with
// them.
func (s *ProvisioningService) PutVirtualModel(ctx context.Context, tenantID model.TenantID, orgID model.OrgID, key string, condition model.MatchCondition, v model.VirtualModelSettings) (model.CommonItem, error) {
	if !validBusinessText(v.Name, v.Description) {
		return model.CommonItem{}, errors.WithStack(port.ErrInvalid)
	}
	if v.CatalogOverrides.IsZero() {
		v.CatalogOverrides = nil
	}
	v.Graph = v.Graph.Normalize()
	scope := model.CommonScope{Family: model.FamilyVirtualModel, TenantID: string(tenantID), OrganizationID: string(orgID)}
	return s.PutCommon(ctx, scope, key, condition, func(ctx context.Context, tx *ProvisioningService) error {
		target, err := tx.businessTarget(ctx, scope, key, v, func() (string, error) {
			vm, err := tx.tx.GetVirtualModelByID(ctx, model.VirtualModelID(key))
			if err != nil {
				return "", err
			}
			return string(vm.OrgID()), nil
		})
		if err != nil || target.unchanged {
			return err
		}
		graph := v.Graph.Graph()
		if err := tx.checkPipeline(ctx, tenantID, orgID, model.FamilyVirtualModel, key, v.Name, graph); err != nil {
			return err
		}
		if err := CheckModelLocalName(ctx, tx.tx, orgID, v.Name); err != nil {
			return err
		}
		var vm virtualModelWriter
		var oldGraph *model.PipelineGraph
		if target.exists {
			current, err := tx.tx.GetVirtualModelByID(ctx, model.VirtualModelID(key))
			if err != nil {
				return errors.WithStack(err)
			}
			writable, ok := current.(virtualModelWriter)
			if !ok {
				return errors.New("virtual model store returns read-only entities")
			}
			vm, oldGraph = writable, current.Graph()
		} else {
			created := model.NewVirtualModel(orgID, v.Name, v.Description)
			created.SetID(model.VirtualModelID(key))
			vm = created
		}
		vm.SetName(v.Name)
		vm.SetDescription(v.Description)
		vm.SetCatalogOverrides(v.CatalogOverrides)
		vm.SetGraph(graph)
		vm.SetUpdatedAt(time.Now())
		if target.exists {
			err = tx.tx.SaveVirtualModel(ctx, vm)
		} else {
			err = tx.tx.CreateVirtualModel(ctx, vm)
		}
		if err != nil {
			return errors.WithStack(err)
		}
		return errors.WithStack(secretcleanup.PruneRemovedNodes(ctx, tx.tx, string(orgID), oldGraph, graph))
	})
}

// PutMiddleware creates the middleware or brings it to the given
// representation. Its targets designate models of the organization.
func (s *ProvisioningService) PutMiddleware(ctx context.Context, tenantID model.TenantID, orgID model.OrgID, key string, condition model.MatchCondition, v model.MiddlewareSettings) (model.CommonItem, error) {
	if !validBusinessText(v.Name, v.Description) {
		return model.CommonItem{}, errors.WithStack(port.ErrInvalid)
	}
	for _, target := range v.Targets {
		if target.Kind != model.ModelRefKindLLM && target.Kind != model.ModelRefKindVirtual {
			return model.CommonItem{}, errors.Wrapf(port.ErrInvalid, "invalid target kind %q", target.Kind)
		}
	}
	v.Targets = model.NormalizeModelRefs(v.Targets)
	v.Graph = v.Graph.Normalize()
	scope := model.CommonScope{Family: model.FamilyMiddleware, TenantID: string(tenantID), OrganizationID: string(orgID)}
	return s.PutCommon(ctx, scope, key, condition, func(ctx context.Context, tx *ProvisioningService) error {
		target, err := tx.businessTarget(ctx, scope, key, v, func() (string, error) {
			mw, err := tx.tx.GetMiddlewareByID(ctx, model.MiddlewareID(key))
			if err != nil {
				return "", err
			}
			return string(mw.OrgID()), nil
		})
		if err != nil || target.unchanged {
			return err
		}
		graph := v.Graph.Graph()
		if err := tx.checkPipeline(ctx, tenantID, orgID, model.FamilyMiddleware, key, v.Name, graph); err != nil {
			return err
		}
		for _, ref := range v.Targets {
			if err := tx.checkModelGrant(ctx, orgID, model.ModelGrantSettings{ModelID: ref.ID, Kind: string(ref.Kind)}); err != nil {
				return err
			}
		}
		var mw middlewareWriter
		var oldGraph *model.PipelineGraph
		if target.exists {
			current, err := tx.tx.GetMiddlewareByID(ctx, model.MiddlewareID(key))
			if err != nil {
				return errors.WithStack(err)
			}
			writable, ok := current.(middlewareWriter)
			if !ok {
				return errors.New("middleware store returns read-only entities")
			}
			mw, oldGraph = writable, current.Graph()
		} else {
			created := model.NewMiddleware(orgID, v.Name, v.Description)
			created.SetID(model.MiddlewareID(key))
			mw = created
		}
		mw.SetName(v.Name)
		mw.SetDescription(v.Description)
		mw.SetEnabled(v.Enabled)
		mw.SetPriority(v.Priority)
		mw.SetAppliesToAll(v.AppliesToAll)
		mw.SetTargets(v.Targets)
		mw.SetGraph(graph)
		mw.SetUpdatedAt(time.Now())
		if target.exists {
			err = tx.tx.SaveMiddleware(ctx, mw)
		} else {
			err = tx.tx.CreateMiddleware(ctx, mw)
		}
		if err != nil {
			return errors.WithStack(err)
		}
		return errors.WithStack(secretcleanup.PruneRemovedNodes(ctx, tx.tx, string(orgID), oldGraph, graph))
	})
}

// checkPipeline validates the graph of a pipeline of the organization, as
// the web UI does, and what only a client choosing its node IDs can break:
// the name is free among the pipelines of the same family, and no other
// pipeline of the organization holds a plugin node with the same plugin and
// ID, since their secrets would be shared, and pruned with either of them.
func (tx *ProvisioningService) checkPipeline(ctx context.Context, tenantID model.TenantID, orgID model.OrgID, family, key, name string, graph *model.PipelineGraph) error {
	if err := ValidatePipelineGraph(graph); err != nil {
		return err
	}
	if err := CheckStaticModelRefs(ctx, tx.orgStore, tx.tx, tenantID, graph); err != nil {
		return err
	}
	vms, err := tx.tx.ListVirtualModels(ctx, orgID)
	if err != nil {
		return errors.WithStack(err)
	}
	mws, err := tx.tx.ListMiddlewares(ctx, orgID)
	if err != nil {
		return errors.WithStack(err)
	}
	type pipeline struct {
		name  string
		graph *model.PipelineGraph
	}
	others := make([]pipeline, 0, len(vms)+len(mws))
	for _, vm := range vms {
		// A virtual model and a middleware may share a client-chosen key:
		// only the pipeline under write is skipped.
		if family == model.FamilyVirtualModel && string(vm.ID()) == key {
			continue
		}
		if family == model.FamilyVirtualModel && vm.Name() == name {
			return errors.Wrapf(port.ErrAlreadyExists, "virtual model %q already exists", name)
		}
		others = append(others, pipeline{vm.Name(), vm.Graph()})
	}
	for _, mw := range mws {
		if family == model.FamilyMiddleware && string(mw.ID()) == key {
			continue
		}
		if family == model.FamilyMiddleware && mw.Name() == name {
			return errors.Wrapf(port.ErrAlreadyExists, "middleware %q already exists", name)
		}
		others = append(others, pipeline{mw.Name(), mw.Graph()})
	}
	placements := model.PluginPlacements(graph)
	for _, other := range others {
		for _, p := range model.PluginPlacements(other.graph) {
			if slices.Contains(placements, p) {
				return errors.Wrapf(port.ErrAlreadyExists, "plugin node %q of plugin %q is already used by %q", p.Node, p.Plugin, other.name)
			}
		}
	}
	return nil
}

// pipelineEntity loads the virtual model or middleware stored under key.
func (tx *ProvisioningService) pipelineEntity(ctx context.Context, family, key string) (model.PipelineEntity, error) {
	if family == model.FamilyMiddleware {
		return tx.tx.GetMiddlewareByID(ctx, model.MiddlewareID(key))
	}
	vm, err := tx.tx.GetVirtualModelByID(ctx, model.VirtualModelID(key))
	if err != nil {
		return nil, err
	}
	entity, ok := vm.(model.PipelineEntity)
	if !ok {
		return nil, errors.New("virtual model store returns read-only entities")
	}
	return entity, nil
}

func (tx *ProvisioningService) savePipelineEntity(ctx context.Context, family string, e model.PipelineEntity) error {
	if family == model.FamilyMiddleware {
		return tx.tx.SaveMiddleware(ctx, e.(model.Middleware))
	}
	return tx.tx.SaveVirtualModel(ctx, e.(model.VirtualModel))
}

func isPipelineFamily(family string) bool {
	return family == model.FamilyVirtualModel || family == model.FamilyMiddleware
}

// ReadPipelineNodes returns the nodes of a virtual model or a middleware,
// sorted by ID, and the ETag of the pipeline: a node has no revision of its
// own. A missing pipeline, or one of another organization, is ErrNotFound.
func (s *ProvisioningService) ReadPipelineNodes(ctx context.Context, scope model.CommonScope, key string) ([]model.PipelineNodeItem, string, error) {
	if !isPipelineFamily(scope.Family) {
		return nil, "", errors.WithStack(port.ErrInvalid)
	}
	var nodes []model.PipelineNodeItem
	var etag string
	err := s.transaction(ctx, func(tx *ProvisioningService) error {
		item, err := tx.tx.ReadProjection(ctx, scope, key)
		if err != nil {
			return errors.WithStack(err)
		}
		etag = item.ETag
		nodes, err = tx.pipelineNodes(ctx, scope, item, "")
		return err
	})
	return nodes, etag, err
}

// ReadPipelineNode returns one node of a virtual model or a middleware, with
// the ETag of the pipeline. Only the secrets of that node are listed.
func (s *ProvisioningService) ReadPipelineNode(ctx context.Context, scope model.CommonScope, key, nodeID string) (model.PipelineNodeItem, string, error) {
	if !isPipelineFamily(scope.Family) {
		return model.PipelineNodeItem{}, "", errors.WithStack(port.ErrInvalid)
	}
	var nodes []model.PipelineNodeItem
	var etag string
	err := s.transaction(ctx, func(tx *ProvisioningService) error {
		item, err := tx.tx.ReadProjection(ctx, scope, key)
		if err != nil {
			return errors.WithStack(err)
		}
		etag = item.ETag
		nodes, err = tx.pipelineNodes(ctx, scope, item, nodeID)
		return err
	})
	if err != nil {
		return model.PipelineNodeItem{}, "", err
	}
	if len(nodes) == 0 {
		return model.PipelineNodeItem{}, "", errors.Wrapf(port.ErrNotFound, "node %q not found", nodeID)
	}
	return nodes[0], etag, nil
}

// pipelineNodes extracts the nodes of a pipeline projection, or the one
// named nodeID when it is set, with the keys of the secrets of its plugin
// nodes.
func (tx *ProvisioningService) pipelineNodes(ctx context.Context, scope model.CommonScope, item model.CommonItem, nodeID string) ([]model.PipelineNodeItem, error) {
	var rep struct {
		Graph model.PipelineGraphSettings `json:"graph"`
	}
	if err := json.Unmarshal(item.Representation, &rep); err != nil {
		return nil, errors.WithStack(err)
	}
	nodes := make([]model.PipelineNodeItem, 0, len(rep.Graph.Nodes))
	for _, n := range rep.Graph.Nodes {
		if nodeID != "" && n.ID != nodeID {
			continue
		}
		node := model.PipelineNodeItem{ID: n.ID, Type: n.Type, Data: n.Data, SecretKeys: []string{}}
		if p := model.PluginPlacements(&model.PipelineGraph{Nodes: []model.PipelineNode{{ID: n.ID, Type: n.Type, Data: n.Data}}}); len(p) == 1 {
			keys, err := tx.tx.ListSecretKeys(ctx, scope.OrganizationID, p[0].Plugin, p[0].Node)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			node.SecretKeys = keys
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// PutPipelineNode replaces the configuration of one node of a virtual model
// or a middleware, under a condition on the ETag of the pipeline, and sets
// or deletes the secrets of a plugin node. The type of the node, the plugin
// of a plugin node, the edges and the positions never change through it. A
// change of secret alone changes no ETag and publishes no event, but is
// audited by a fingerprint.
func (s *ProvisioningService) PutPipelineNode(ctx context.Context, scope model.CommonScope, key, nodeID string, condition model.MatchCondition, update model.PipelineNodeUpdate) (model.PipelineNodeItem, string, error) {
	if !isPipelineFamily(scope.Family) {
		return model.PipelineNodeItem{}, "", errors.WithStack(port.ErrInvalid)
	}
	if len(update.Secrets) > maxNodeSecrets {
		return model.PipelineNodeItem{}, "", errors.Wrapf(port.ErrInvalid, "a node holds at most %d secrets", maxNodeSecrets)
	}
	for name, value := range update.Secrets {
		if name == "" || strings.TrimSpace(name) != name || len(name) > maxNodeSecretKeyLength {
			return model.PipelineNodeItem{}, "", errors.Wrapf(port.ErrInvalid, "invalid secret key %q", name)
		}
		if value != nil && len(*value) > maxProviderKeyLength {
			return model.PipelineNodeItem{}, "", errors.Wrapf(port.ErrInvalid, "secret %q is too long", name)
		}
	}
	tenantID := model.TenantID(scope.TenantID)
	orgID := model.OrgID(scope.OrganizationID)
	var secretKeys []string
	item, err := s.PutCommon(ctx, scope, key, condition, func(ctx context.Context, tx *ProvisioningService) error {
		if _, err := tx.tx.ReadProjection(ctx, scope, key); err != nil {
			return errors.WithStack(err)
		}
		entity, err := tx.pipelineEntity(ctx, scope.Family, key)
		if err != nil {
			return errors.WithStack(err)
		}
		graph := entity.Graph()
		index := -1
		if graph != nil {
			index = slices.IndexFunc(graph.Nodes, func(n model.PipelineNode) bool { return n.ID == nodeID })
		}
		if index < 0 {
			return errors.Wrapf(port.ErrNotFound, "node %q not found", nodeID)
		}
		current := graph.Nodes[index]
		next := current
		next.Data = update.Data
		if trimmed := bytes.TrimSpace(next.Data); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			next.Data = json.RawMessage("{}")
		}
		if err := ValidatePipelineNode(next); err != nil {
			return err
		}
		// The placement of a node is invariant here, which spares the
		// placement rule of checkPipeline: its type never changes and a
		// change of plugin is refused. Widening PipelineNodeUpdate to the
		// type or the plugin requires checking placements again.
		placement := model.PluginPlacements(&model.PipelineGraph{Nodes: []model.PipelineNode{current}})
		if current.Type == model.NodeTypePlugin {
			after := model.PluginPlacements(&model.PipelineGraph{Nodes: []model.PipelineNode{next}})
			if len(placement) != 1 || len(after) != 1 || after[0] != placement[0] {
				return errors.Wrap(port.ErrNotAllowed, "the plugin of a node can not change")
			}
		} else if len(update.Secrets) > 0 {
			return errors.Wrap(port.ErrInvalid, "only a plugin node holds secrets")
		}
		changed, err := jsonDiffers(current.Data, next.Data)
		if err != nil {
			return err
		}
		writes, err := tx.nodeSecretWrites(ctx, string(orgID), placement, update.Secrets)
		if err != nil {
			return err
		}
		// A change of secret alone changes no projection, so the store does
		// not check the write authority of the family: it is checked here.
		if len(writes) > 0 && !tx.OwnershipPolicy().Allows(scope.Family, model.WriteAuthority(ctx)) {
			return errors.Wrapf(port.ErrOwnershipDenied, "%s", scope.Family)
		}
		if changed || len(writes) > 0 {
			// A configuration equal to the stored one keeps its bytes: a
			// change of secret alone must leave the representation as is.
			if changed {
				graph.Nodes[index] = next
			}
			if changed {
				if err := ValidatePipelineGraph(graph); err != nil {
					return err
				}
				if err := CheckStaticModelRefs(ctx, tx.orgStore, tx.tx, tenantID, graph); err != nil {
					return err
				}
			}
			entity.SetGraph(graph)
			entity.SetUpdatedAt(time.Now())
			// Saved even for a change of secret alone: the write designates
			// the pipeline to the audit, which then records the fingerprint
			// of its secrets.
			if err := tx.savePipelineEntity(ctx, scope.Family, entity); err != nil {
				return errors.WithStack(err)
			}
			for _, write := range writes {
				if err := write(ctx); err != nil {
					return errors.WithStack(err)
				}
			}
		}
		if len(placement) == 1 {
			if secretKeys, err = tx.tx.ListSecretKeys(ctx, string(orgID), placement[0].Plugin, placement[0].Node); err != nil {
				return errors.WithStack(err)
			}
		}
		return nil
	})
	if err != nil {
		return model.PipelineNodeItem{}, "", err
	}
	var rep struct {
		Graph model.PipelineGraphSettings `json:"graph"`
	}
	if err := json.Unmarshal(item.Representation, &rep); err != nil {
		return model.PipelineNodeItem{}, "", errors.WithStack(err)
	}
	for _, n := range rep.Graph.Nodes {
		if n.ID == nodeID {
			if secretKeys == nil {
				secretKeys = []string{}
			}
			return model.PipelineNodeItem{ID: n.ID, Type: n.Type, Data: n.Data, SecretKeys: secretKeys}, item.ETag, nil
		}
	}
	return model.PipelineNodeItem{}, "", errors.Wrapf(port.ErrNotFound, "node %q not found", nodeID)
}

// nodeSecretWrites returns the writes bringing the secrets of a plugin node
// to secrets: a value sets its key, nil deletes it. A value equal to the
// stored one is not encrypted again, so a replay changes nothing.
func (tx *ProvisioningService) nodeSecretWrites(ctx context.Context, scopeID string, placement []model.PluginPlacement, secrets map[string]*string) ([]func(context.Context) error, error) {
	if len(placement) != 1 || len(secrets) == 0 {
		return nil, nil
	}
	plugin, node := placement[0].Plugin, placement[0].Node
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	slices.Sort(names)
	var writes []func(context.Context) error
	for _, name := range names {
		stored, found, err := tx.tx.GetSecret(ctx, scopeID, plugin, node, name)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		value := secrets[name]
		if value == nil {
			if found {
				writes = append(writes, func(ctx context.Context) error { return tx.tx.DeleteSecret(ctx, scopeID, plugin, node, name) })
			}
			continue
		}
		if found {
			plain, err := crypto.Decrypt(tx.secretKey, stored)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			if plain == *value {
				continue
			}
		}
		encrypted, err := crypto.Encrypt(tx.secretKey, *value)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		writes = append(writes, func(ctx context.Context) error { return tx.tx.SetSecret(ctx, scopeID, plugin, node, name, encrypted) })
	}
	return writes, nil
}

// jsonDiffers compares two JSON values regardless of formatting and key
// order; a missing value is an empty object.
func jsonDiffers(a, b json.RawMessage) (bool, error) {
	ca, err := model.CanonicalJSON(a)
	if err != nil {
		return false, errors.WithStack(err)
	}
	cb, err := model.CanonicalJSON(b)
	if err != nil {
		return false, errors.WithStack(err)
	}
	return !bytes.Equal(ca, cb), nil
}
