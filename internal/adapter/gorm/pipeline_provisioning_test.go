package gorm_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/crypto"
	gormpkg "gorm.io/gorm"
)

// TestPipelineNodes reads and edits the nodes of a virtual model: an edit
// conflicts with a stale ETag, publishes an update of the virtual model and
// leaves the rest of the graph untouched.
func TestPipelineNodes(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		svc := businessService(store)
		fixture := newOwnershipFixture(t, store)
		key := uuid.NewString()
		scope := model.CommonScope{Family: model.FamilyVirtualModel, TenantID: string(testTenantID), OrganizationID: string(fixture.org)}

		created, err := svc.PutVirtualModel(ctx, testTenantID, fixture.org, key, noCondition, virtualModelSettings("router", 0.5))
		require.NoError(t, err)
		require.NotContains(t, string(created.Representation), "fingerprint")

		nodes, etag, err := svc.ReadPipelineNodes(ctx, scope, key)
		require.NoError(t, err)
		require.Equal(t, created.ETag, etag)
		ids := []string{}
		for _, node := range nodes {
			ids = append(ids, node.ID)
			require.Equal(t, []string{}, node.SecretKeys)
		}
		require.Equal(t, []string{"gen", "guard", "model", "sink"}, ids)

		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		update := model.PipelineNodeUpdate{Data: json.RawMessage(`{"pluginName":"guard","config":{"threshold":0.9}}`)}
		node, next, err := svc.PutPipelineNode(ctx, scope, key, "guard", ifMatch(t, etag), update)
		require.NoError(t, err)
		require.NotEqual(t, etag, next)
		require.JSONEq(t, `{"pluginName":"guard","config":{"threshold":0.9}}`, string(node.Data))
		events, _ := feedSince(t, store, cursor)
		require.Equal(t, []string{"virtual_model.updated.v1"}, eventTypes(events))
		require.Equal(t, next, events[0].Data.ETag)

		vm, err := store.GetVirtualModelByID(ctx, model.VirtualModelID(key))
		require.NoError(t, err)
		want := virtualModelSettings("router", 0.9).Graph.Normalize()
		require.Equal(t, want, model.PipelineGraphSettingsOf(vm.Graph()), "only the configuration of the node changed")

		// The ETag of the pipeline guards its nodes.
		_, _, err = svc.PutPipelineNode(ctx, scope, key, "guard", ifMatch(t, etag), update)
		require.ErrorIs(t, err, port.ErrPreconditionFailed)
		_, err = svc.PutVirtualModel(ctx, testTenantID, fixture.org, key, ifMatch(t, etag), virtualModelSettings("router", 0.1))
		require.ErrorIs(t, err, port.ErrPreconditionFailed, "a node edit conflicts with a graph rewrite")

		// A replay changes nothing.
		published := eventCount(t, db)
		_, replayed, err := svc.PutPipelineNode(ctx, scope, key, "guard", ifMatch(t, next), model.PipelineNodeUpdate{Data: json.RawMessage(`{"config":{"threshold":0.9},"pluginName":"guard"}`)})
		require.NoError(t, err)
		require.Equal(t, next, replayed)
		require.Equal(t, published, eventCount(t, db))

		for name, tc := range map[string]struct {
			node   string
			update string
			err    error
		}{
			"missing node":      {"nope", `{}`, port.ErrNotFound},
			"plugin change":     {"guard", `{"pluginName":"other"}`, port.ErrNotAllowed},
			"no object":         {"model", `[1]`, port.ErrInvalid},
			"secret on builtin": {"model", `{"passthrough":true}`, port.ErrInvalid},
		} {
			update := model.PipelineNodeUpdate{Data: json.RawMessage(tc.update)}
			if name == "secret on builtin" {
				update.Secrets = map[string]*string{"token": ptrTo("x")}
			}
			_, _, err := svc.PutPipelineNode(ctx, scope, key, tc.node, noCondition, update)
			require.ErrorIs(t, err, tc.err, name)
		}

		_, _, err = svc.ReadPipelineNodes(ctx, model.CommonScope{Family: model.FamilyMiddleware, TenantID: scope.TenantID, OrganizationID: scope.OrganizationID}, key)
		require.ErrorIs(t, err, port.ErrNotFound, "a virtual model is no middleware")
	})
}

// TestPipelineNodeSecrets keeps the secrets of a plugin node write-only: a
// change of secret changes no ETag and publishes nothing, but is audited by
// a fingerprint; removing the node or the pipeline removes its secrets.
func TestPipelineNodeSecrets(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		svc := businessService(store)
		fixture := newOwnershipFixture(t, store)
		key := uuid.NewString()
		scope := model.CommonScope{Family: model.FamilyVirtualModel, TenantID: string(testTenantID), OrganizationID: string(fixture.org)}
		created, err := svc.PutVirtualModel(ctx, testTenantID, fixture.org, key, noCondition, virtualModelSettings("router", 0.5))
		require.NoError(t, err)

		data := json.RawMessage(`{"pluginName":"guard","config":{"threshold":0.5}}`)
		audits, published := auditCount(t, db), eventCount(t, db)
		node, etag, err := svc.PutPipelineNode(ctx, scope, key, "guard", noCondition, model.PipelineNodeUpdate{Data: data, Secrets: map[string]*string{"token": ptrTo("s3cr3t-value"), "other": nil}})
		require.NoError(t, err)
		require.Equal(t, created.ETag, etag)
		require.Equal(t, []string{"token"}, node.SecretKeys)
		require.Equal(t, published, eventCount(t, db))
		require.Equal(t, audits+1, auditCount(t, db))
		var audit xologorm.MutationAudit
		require.NoError(t, db.Order("created_at DESC").First(&audit).Error)
		require.Equal(t, "virtual_model", audit.Resource)
		require.NotContains(t, audit.Before+audit.After, "s3cr3t-value")
		require.Contains(t, audit.After, "secrets_fingerprint")

		stored, found, err := store.GetSecret(ctx, string(fixture.org), "guard", "guard", "token")
		require.NoError(t, err)
		require.True(t, found)
		plain, err := crypto.Decrypt(businessSecretKey, stored)
		require.NoError(t, err)
		require.Equal(t, "s3cr3t-value", plain)

		nodes, _, err := svc.ReadPipelineNodes(ctx, scope, key)
		require.NoError(t, err)
		for _, n := range nodes {
			require.NotContains(t, string(n.Data), "s3cr3t")
		}

		// The same value is not encrypted again: a replay audits nothing.
		audits = auditCount(t, db)
		_, _, err = svc.PutPipelineNode(ctx, scope, key, "guard", noCondition, model.PipelineNodeUpdate{Data: data, Secrets: map[string]*string{"token": ptrTo("s3cr3t-value")}})
		require.NoError(t, err)
		require.Equal(t, audits, auditCount(t, db))

		// Nor does a configuration in another formatting: a change of
		// secret alone moves no ETag and publishes nothing.
		reordered := json.RawMessage(`{ "config": {"threshold": 0.5}, "pluginName": "guard" }`)
		published = eventCount(t, db)
		_, rotated, err := svc.PutPipelineNode(ctx, scope, key, "guard", noCondition, model.PipelineNodeUpdate{Data: reordered, Secrets: map[string]*string{"token": ptrTo("s3cr3t-value"), "extra": ptrTo("x")}})
		require.NoError(t, err)
		require.Equal(t, created.ETag, rotated)
		require.Equal(t, published, eventCount(t, db))
		_, _, err = svc.PutPipelineNode(ctx, scope, key, "guard", noCondition, model.PipelineNodeUpdate{Data: data, Secrets: map[string]*string{"extra": nil}})
		require.NoError(t, err)

		// And a replay of the pipeline in another formatting is a no-op.
		replay := virtualModelSettings("router", 0.5)
		for i := range replay.Graph.Nodes {
			if replay.Graph.Nodes[i].ID == "guard" {
				replay.Graph.Nodes[i].Data = reordered
			}
		}
		again, err := svc.PutVirtualModel(ctx, testTenantID, fixture.org, key, noCondition, replay)
		require.NoError(t, err)
		require.Equal(t, created.ETag, again.ETag)
		require.Equal(t, published, eventCount(t, db))

		// Another pipeline can not hold the same plugin node: they would
		// share its secrets.
		_, err = svc.PutVirtualModel(ctx, testTenantID, fixture.org, uuid.NewString(), noCondition, virtualModelSettings("copy", 0.5))
		require.ErrorIs(t, err, port.ErrAlreadyExists)
		// Even a middleware under the same key as the virtual model.
		_, err = svc.PutMiddleware(ctx, testTenantID, fixture.org, key, noCondition, model.MiddlewareSettings{Name: "same key", AppliesToAll: true, Graph: pipelineGraph(0.5)})
		require.ErrorIs(t, err, port.ErrAlreadyExists)

		// A change of secret alone obeys the write authority of the family,
		// though it changes no projection.
		owned := service.NewProvisioningService(store, store, store, store,
			service.WithProvisioningTransaction(store), service.WithProvisioningReader(store),
			service.WithMultiTenant(true), service.WithSecretKey(businessSecretKey),
			service.WithOwnershipPolicy(model.OwnershipPolicy{model.FamilyVirtualModel: model.OwnerControlPlane}))
		_, _, err = owned.PutPipelineNode(ctx, scope, key, "guard", noCondition, model.PipelineNodeUpdate{Data: data, Secrets: map[string]*string{"token": ptrTo("rotated")}})
		require.ErrorIs(t, err, port.ErrOwnershipDenied)
		_, _, err = owned.PutPipelineNode(ctx, scope, key, "guard", noCondition, model.PipelineNodeUpdate{Data: data, Secrets: map[string]*string{"token": ptrTo("s3cr3t-value")}})
		require.NoError(t, err, "a replay writes nothing")

		// Removing the node removes its secrets.
		without := virtualModelSettings("router", 0.5)
		without.Graph.Nodes = slicesDeleteNode(without.Graph.Nodes, "guard")
		without.Graph.Edges = []model.PipelineEdgeSettings{{ID: "gen-model", Source: "gen", SourcePort: "request", Target: "model", TargetPort: "request"},
			{ID: "model-sink", Source: "model", SourcePort: "response", Target: "sink", TargetPort: "response"}}
		_, err = svc.PutVirtualModel(ctx, testTenantID, fixture.org, key, noCondition, without)
		require.NoError(t, err)
		_, found, err = store.GetSecret(ctx, string(fixture.org), "guard", "guard", "token")
		require.NoError(t, err)
		require.False(t, found)

		// And so does deleting the pipeline.
		_, err = svc.PutVirtualModel(ctx, testTenantID, fixture.org, key, noCondition, virtualModelSettings("router", 0.5))
		require.NoError(t, err)
		_, _, err = svc.PutPipelineNode(ctx, scope, key, "guard", noCondition, model.PipelineNodeUpdate{Data: data, Secrets: map[string]*string{"token": ptrTo("again")}})
		require.NoError(t, err)
		require.NoError(t, svc.DeleteBusiness(ctx, scope, key, noCondition))
		_, found, err = store.GetSecret(ctx, string(fixture.org), "guard", "guard", "token")
		require.NoError(t, err)
		require.False(t, found)
		_, err = svc.GetCommon(ctx, scope, key)
		require.ErrorIs(t, err, port.ErrNotFound)
	})
}

// TestPipelineValidation refuses what the web UI refuses, and what only a
// client choosing its node IDs or targets could write.
func TestPipelineValidation(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		svc := businessService(store)
		fixture := newOwnershipFixture(t, store)
		other := newOwnershipFixture(t, store)
		f := newBusinessFixture(t, store, svc, other)

		for name, mutate := range map[string]func(*model.VirtualModelSettings){
			"duplicate node":   func(v *model.VirtualModelSettings) { v.Graph.Nodes[1].ID = "gen" },
			"unknown type":     func(v *model.VirtualModelSettings) { v.Graph.Nodes[0].Type = "teleport" },
			"dangling edge":    func(v *model.VirtualModelSettings) { v.Graph.Edges[0].Target = "nowhere" },
			"plugin unnamed":   func(v *model.VirtualModelSettings) { v.Graph.Nodes[1].Data = json.RawMessage(`{}`) },
			"data no object":   func(v *model.VirtualModelSettings) { v.Graph.Nodes[2].Data = json.RawMessage(`"x"`) },
			"empty name":       func(v *model.VirtualModelSettings) { v.Name = "" },
			"padded node id":   func(v *model.VirtualModelSettings) { v.Graph.Nodes[0].ID = " gen" },
			"overlong node id": func(v *model.VirtualModelSettings) { v.Graph.Nodes[0].ID = strings.Repeat("n", 201) },
		} {
			v := virtualModelSettings("invalid", 0.5)
			mutate(&v)
			_, err := svc.PutVirtualModel(ctx, testTenantID, fixture.org, uuid.NewString(), noCondition, v)
			require.ErrorIs(t, err, port.ErrInvalid, name)
		}

		// A middleware wraps models of its own organization only.
		_, err := svc.PutMiddleware(ctx, testTenantID, fixture.org, uuid.NewString(), noCondition, middlewareSettings(f.llm))
		require.ErrorIs(t, err, port.ErrParentNotFound)
		bad := middlewareSettings(f.llm)
		bad.Targets[0].Kind = "plugin"
		_, err = svc.PutMiddleware(ctx, testTenantID, other.org, uuid.NewString(), noCondition, bad)
		require.ErrorIs(t, err, port.ErrInvalid)

		// A virtual model of another organization is not found here.
		_, err = svc.PutVirtualModel(ctx, testTenantID, fixture.org, f.virtualModel, noCondition, virtualModelSettings("router", 0.5))
		require.ErrorIs(t, err, port.ErrNotFound)
		_, _, err = svc.ReadPipelineNodes(ctx, model.CommonScope{Family: model.FamilyVirtualModel, TenantID: string(testTenantID), OrganizationID: string(fixture.org)}, f.virtualModel)
		require.ErrorIs(t, err, port.ErrNotFound)

		// Names are unique per organization.
		_, err = svc.PutVirtualModel(ctx, testTenantID, other.org, uuid.NewString(), noCondition, model.VirtualModelSettings{Name: "router"})
		require.ErrorIs(t, err, port.ErrAlreadyExists)
	})
}

// TestPipelineLocalWrites publishes the writes of the web UI to virtual
// models and middlewares.
func TestPipelineLocalWrites(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		fixture := newOwnershipFixture(t, store)
		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)

		vm := model.NewVirtualModel(fixture.org, "local", "")
		require.NoError(t, store.CreateVirtualModel(ctx, vm))
		vm.SetDescription("edited")
		require.NoError(t, store.SaveVirtualModel(ctx, vm))
		mw := model.NewMiddleware(fixture.org, "local", "")
		require.NoError(t, store.CreateMiddleware(ctx, mw))
		require.NoError(t, store.DeleteMiddleware(ctx, mw.ID()))
		require.NoError(t, store.DeleteVirtualModel(ctx, vm.ID()))

		events, _ := feedSince(t, store, cursor)
		require.Equal(t, []string{
			"virtual_model.created.v1", "virtual_model.updated.v1", "middleware.created.v1", "middleware.deleted.v1", "virtual_model.deleted.v1",
		}, eventTypes(events))
		require.Equal(t, string(vm.ID()), events[0].Data.Key.ResourceID, "a resource created locally keeps its identifier")

		owned := ownedStore(t, db, model.OwnershipPolicy{model.FamilyVirtualModel: model.OwnerControlPlane})
		require.ErrorIs(t, owned.CreateVirtualModel(ctx, model.NewVirtualModel(fixture.org, "refused", "")), port.ErrOwnershipDenied)
	})
}

func slicesDeleteNode(nodes []model.PipelineNodeSettings, id string) []model.PipelineNodeSettings {
	out := []model.PipelineNodeSettings{}
	for _, n := range nodes {
		if n.ID != id {
			out = append(out, n)
		}
	}
	return out
}
