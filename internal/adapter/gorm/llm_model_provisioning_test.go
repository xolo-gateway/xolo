package gorm_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	gormpkg "gorm.io/gorm"
)

// TestLLMModelProjection publishes the models written locally as the
// read-only llm_model family: only what designates a model changes its
// projection, and a provider deletion publishes the deletion of its models.
func TestLLMModelProjection(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		svc := businessService(store)
		fixture := newOwnershipFixture(t, store)
		f := newBusinessFixture(t, store, svc, fixture)
		scope := model.CommonScope{Family: model.FamilyLLMModel, TenantID: string(testTenantID), OrganizationID: string(fixture.org)}

		llm, err := store.GetLLMModelByID(ctx, f.llm)
		require.NoError(t, err)
		created, err := svc.GetCommon(ctx, scope, string(f.llm))
		require.NoError(t, err)
		require.JSONEq(t, `{"provider_id":"`+f.provider+`","proxy_name":"`+llm.ProxyName()+`","kind":"completion","enabled":true}`, string(created.Representation))

		// Neither the routing nor the pricing are part of the projection.
		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)
		rerouted := model.NewLLMModel(llm.ProviderID(), llm.OrgID(), llm.ProxyName(), "gpt-4o-2024-08-06", "Faster", 3, 9)
		require.NoError(t, store.SaveLLMModel(ctx, keptID{rerouted, llm.ID()}))
		events, cursor := feedSince(t, store, cursor)
		require.Empty(t, events)
		unchanged, err := svc.GetCommon(ctx, scope, string(f.llm))
		require.NoError(t, err)
		require.Equal(t, created.ETag, unchanged.ETag)

		// A disabled model stays exposed: targets and grants may designate it.
		disabled := model.NewLLMModel(llm.ProviderID(), llm.OrgID(), llm.ProxyName(), "gpt-4o-2024-08-06", "Faster", 3, 9)
		disabled.SetEnabled(false)
		require.NoError(t, store.SaveLLMModel(ctx, keptID{disabled, llm.ID()}))
		events, cursor = feedSince(t, store, cursor)
		require.Equal(t, []string{"llm_model.updated.v1"}, eventTypes(events))
		require.Equal(t, model.CommonKey{TenantID: string(testTenantID), OrganizationID: string(fixture.org), ResourceID: string(f.llm)}, events[0].Data.Key)
		updated, err := svc.GetCommon(ctx, scope, string(f.llm))
		require.NoError(t, err)
		require.NotEqual(t, created.ETag, updated.ETag)
		var rep map[string]any
		require.NoError(t, json.Unmarshal(updated.Representation, &rep))
		require.Equal(t, false, rep["enabled"])

		// The API never writes a model.
		require.ErrorIs(t, svc.DeleteBusiness(ctx, scope, string(f.llm), noCondition), port.ErrInvalid)
		_, err = svc.PutCommon(ctx, scope, string(f.llm), noCondition, func(context.Context, *service.ProvisioningService) error { return nil })
		require.ErrorIs(t, err, port.ErrInvalid)

		second := model.NewLLMModel(llm.ProviderID(), llm.OrgID(), "embed", "text-embedding-3-small", "", 0, 0)
		second.SetKind(model.ModelKindEmbeddings)
		require.NoError(t, store.CreateLLMModel(ctx, second))
		require.NoError(t, store.DeleteLLMModel(ctx, second.ID()))
		events, cursor = feedSince(t, store, cursor)
		require.Equal(t, []string{"llm_model.created.v1", "llm_model.deleted.v1"}, eventTypes(events))

		// Deleting the provider removes its models, children first.
		require.NoError(t, store.DeleteProvider(ctx, model.ProviderID(f.provider)))
		events, _ = feedSince(t, store, cursor)
		require.Equal(t, []string{"llm_model.deleted.v1", "provider.deleted.v1"}, eventTypes(events))
		_, err = svc.GetCommon(ctx, scope, string(f.llm))
		require.ErrorIs(t, err, port.ErrNotFound)
	})
}

// keptID saves a new state of an existing model, as the web UI does.
type keptID struct {
	*model.BaseLLMModel
	id model.LLMModelID
}

func (m keptID) ID() model.LLMModelID { return m.id }
