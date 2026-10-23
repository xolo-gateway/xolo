package gorm_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	gormpkg "gorm.io/gorm"
)

// TestPersonalVirtualModelProjection publishes the personal virtual models
// written from the profile as the read-only personal_virtual_model family,
// hanging from their member, and the removal of a member's models with it.
func TestPersonalVirtualModelProjection(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gormpkg.DB) {
		ctx := t.Context()
		store := newSeededStore(t, db)
		svc := businessService(store)
		fixture := newOwnershipFixture(t, store)
		other := newOwnershipFixture(t, store)
		scope := model.CommonScope{Family: model.FamilyPersonalVirtualModel, TenantID: string(testTenantID), MemberID: string(fixture.user)}
		cursor, err := store.CaptureEventCursor(ctx)
		require.NoError(t, err)

		var graph model.PipelineGraph
		require.NoError(t, json.Unmarshal([]byte(`{"nodes":[{"id":"gen","type":"generator","position":{"x":0,"y":0}}],"edges":[]}`), &graph))
		vm := model.NewPersonalVirtualModel(fixture.user, "router", "")
		vm.SetGraph(&graph)
		require.NoError(t, store.CreatePersonalVirtualModel(ctx, vm))
		key := string(vm.ID())

		events, cursor := feedSince(t, store, cursor)
		require.Equal(t, []string{"personal_virtual_model.created.v1"}, eventTypes(events))
		require.Equal(t, model.CommonKey{TenantID: string(testTenantID), MemberID: string(fixture.user), ResourceID: key}, events[0].Data.Key)
		created, err := svc.GetCommon(ctx, scope, key)
		require.NoError(t, err)
		require.JSONEq(t, `{"name":"router","description":"","team_id":"","catalog_overrides":null,"graph":{"nodes":[{"id":"gen","type":"generator","position":{"x":0,"y":0},"data":{}}],"edges":[]}}`, string(created.Representation))

		page, err := svc.ListCommon(ctx, scope, "", 100)
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		require.Equal(t, model.CommonKey{TenantID: string(testTenantID), MemberID: string(fixture.user), ResourceID: key}, page.Items[0].Key)
		page, err = svc.ListCommon(ctx, model.CommonScope{Family: model.FamilyPersonalVirtualModel, TenantID: string(testTenantID), MemberID: string(other.user)}, "", 100)
		require.NoError(t, err)
		require.Empty(t, page.Items, "another member holds none")
		_, err = svc.GetCommon(ctx, model.CommonScope{Family: model.FamilyPersonalVirtualModel, TenantID: string(testTenantID), MemberID: string(other.user)}, key)
		require.ErrorIs(t, err, port.ErrNotFound)
		_, err = svc.ListCommon(ctx, model.CommonScope{Family: model.FamilyPersonalVirtualModel, TenantID: string(testTenantID), MemberID: uuid.NewString()}, "", 100)
		require.ErrorIs(t, err, port.ErrParentNotFound)

		// The API never writes a personal model.
		require.ErrorIs(t, svc.DeleteBusiness(ctx, scope, key, noCondition), port.ErrInvalid)
		_, err = svc.PutCommon(ctx, scope, key, noCondition, func(context.Context, *service.ProvisioningService) error { return nil })
		require.ErrorIs(t, err, port.ErrInvalid)

		vm.SetName("renamed")
		require.NoError(t, store.SavePersonalVirtualModel(ctx, vm))
		events, cursor = feedSince(t, store, cursor)
		require.Equal(t, []string{"personal_virtual_model.updated.v1"}, eventTypes(events))
		updated, err := svc.GetCommon(ctx, scope, key)
		require.NoError(t, err)
		require.NotEqual(t, created.ETag, updated.ETag)

		second := model.NewPersonalVirtualModel(fixture.user, "draft", "")
		require.NoError(t, store.CreatePersonalVirtualModel(ctx, second))
		require.NoError(t, store.DeletePersonalVirtualModel(ctx, second.ID()))
		events, cursor = feedSince(t, store, cursor)
		require.Equal(t, []string{"personal_virtual_model.created.v1", "personal_virtual_model.deleted.v1"}, eventTypes(events))

		// Deleting the member removes its models, children first.
		require.NoError(t, store.DeleteUser(ctx, other.user))
		events, cursor = feedSince(t, store, cursor)
		require.NotContains(t, eventTypes(events), "personal_virtual_model.deleted.v1")
		require.NoError(t, store.DeleteUser(ctx, fixture.user))
		events, _ = feedSince(t, store, cursor)
		require.Equal(t, "personal_virtual_model.deleted.v1", events[0].Type)
		require.Contains(t, eventTypes(events), "member.deleted.v1")
		_, err = svc.GetCommon(ctx, scope, key)
		require.ErrorIs(t, err, port.ErrNotFound)
	})
}
