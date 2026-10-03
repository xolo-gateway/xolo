package gorm_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

func finishTestPurge(t *testing.T, s *adapter.Store, scope model.CommonScope, key string) error {
	t.Helper()
	d, err := s.ReadDeletion(t.Context(), scope, key)
	if err != nil {
		return err
	}
	// Scheduling must retain the representation and requires an external receipt.
	_, err = s.ReadCommon(t.Context(), scope, key)
	require.NoError(t, err)
	require.Nil(t, d.ConfirmedAt)
	raw, err := s.ExportDeletion(t.Context(), scope, key)
	if err != nil {
		return err
	}
	var envelope struct{ SHA256 string }
	require.NoError(t, json.Unmarshal(raw, &envelope))
	c, err := model.ParseMatchCondition([]string{d.ETag})
	require.NoError(t, err)
	_, err = s.ConfirmDeletion(t.Context(), scope, key, c, envelope.SHA256)
	if err != nil {
		return err
	}
	time.Sleep(max(0, time.Until(d.PurgeAfter)+time.Millisecond))
	return s.PurgeDeletion(t.Context(), scope, key)
}
func deleteAndPurgeTenant(t *testing.T, s *adapter.Store, id model.TenantID) error {
	s.ConfigureLifecycle(true, time.Second)
	if err := s.DeleteTenant(t.Context(), id); err != nil {
		return err
	}
	return finishTestPurge(t, s, model.CommonScope{Family: "tenant"}, string(id))
}
func deleteAndPurgeOrg(t *testing.T, s *adapter.Store, id model.OrgID) error {
	s.ConfigureLifecycle(true, time.Second)
	org, err := s.GetOrgByID(t.Context(), id)
	if err != nil {
		return err
	}
	if err = s.DeleteOrg(t.Context(), id); err != nil {
		return err
	}
	return finishTestPurge(t, s, model.CommonScope{Family: "organization", TenantID: string(org.TenantID())}, string(id))
}
func deleteAndPurgeUser(t *testing.T, s *adapter.Store, id model.UserID) error {
	s.ConfigureLifecycle(true, time.Second)
	user, err := s.GetUserByID(t.Context(), id)
	if err != nil {
		return err
	}
	if err = s.DeleteUser(t.Context(), id); err != nil {
		return err
	}
	return finishTestPurge(t, s, model.CommonScope{Family: "member", TenantID: string(user.TenantID())}, string(id))
}
