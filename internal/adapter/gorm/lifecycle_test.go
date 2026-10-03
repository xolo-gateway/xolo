package gorm_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"gorm.io/gorm"
)

func TestLifecycleFrozenExportConfirmationAndPurge(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		ctx := t.Context()
		s := adapter.NewStore(db)
		require.NoError(t, s.Migrate(ctx))
		api := service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))
		tid := model.NewTenantID()
		scope := model.CommonScope{Family: "tenant"}
		key := string(tid)
		_, err := api.PutCommonTenant(ctx, tid, service.CommonResource{Slug: "retired", Name: "Retired", Status: "active"})
		require.NoError(t, err)
		oid := model.NewOrgID()
		_, err = api.PutCommonOrganization(ctx, tid, oid, service.CommonResource{Slug: "team", Name: "Team", Status: "active"})
		require.NoError(t, err)
		uid := model.NewUserID()
		_, err = api.PutCommonMember(ctx, tid, uid, service.CommonMember{Email: "owner@test.local", TenantRole: "owner", Status: "active"})
		require.NoError(t, err)
		app := model.NewApplication(oid, "app", "description", true)
		require.NoError(t, s.CreateApplication(ctx, app))
		other := model.NewTenantID()
		_, err = api.PutCommonTenant(ctx, other, service.CommonResource{Slug: "other", Name: "Other", Status: "active"})
		require.NoError(t, err)
		before, err := s.ReadCommon(ctx, scope, key)
		require.NoError(t, err)
		d, err := s.ScheduleDeletion(ctx, scope, key, model.MatchCondition{}, time.Second)
		require.NoError(t, err)
		require.NotEqual(t, before.ETag, d.ETag)
		repeated, err := s.ScheduleDeletion(ctx, scope, key, model.MatchCondition{}, time.Hour)
		require.NoError(t, err)
		require.Equal(t, d, repeated)
		item, err := s.ReadCommon(ctx, scope, key)
		require.NoError(t, err)
		require.Contains(t, string(item.Representation), `"status":"deleted"`)
		require.ErrorIs(t, s.PurgeDeletion(ctx, scope, key), port.ErrPurgeNotReady)
		require.ErrorIs(t, s.CreateApplication(ctx, model.NewApplication(oid, "late", "", true)), port.ErrResourceDeleted)
		// The database guard also covers a writer outside the store and across connections.
		require.ErrorContains(t, db.Create(&adapter.Application{ID: "late", OrgID: string(oid), Name: "late"}).Error, "xolo_resource_deleted")
		_, err = api.WriteCommon(ctx, scope, key, model.MatchCondition{}, func(tx *service.ProvisioningService) error {
			_, e := tx.PutCommonTenant(ctx, tid, service.CommonResource{Slug: "retired", Name: "Retired", Status: "active"})
			return e
		})
		require.ErrorIs(t, err, port.ErrResourceDeleted)
		raw, err := s.ExportDeletion(ctx, scope, key)
		require.NoError(t, err)
		var envelope struct {
			SHA256  string `json:"sha256"`
			Payload struct {
				Complete bool                        `json:"complete"`
				Tables   map[string][]map[string]any `json:"tables"`
			} `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(raw, &envelope))
		require.True(t, envelope.Payload.Complete)
		require.Len(t, envelope.Payload.Tables["applications"], 1)
		require.Len(t, envelope.Payload.Tables["users"], 1)
		c, err := model.ParseMatchCondition([]string{d.ETag})
		require.NoError(t, err)
		_, err = s.ConfirmDeletion(ctx, scope, key, model.MatchCondition{}, envelope.SHA256)
		require.ErrorIs(t, err, port.ErrConfirmationRequired)
		_, err = s.ConfirmDeletion(ctx, scope, key, c, strings.Repeat("0", 64))
		require.ErrorIs(t, err, port.ErrExportMismatch)
		_, err = s.ConfirmDeletion(ctx, scope, key, c, envelope.SHA256)
		require.NoError(t, err)
		require.ErrorIs(t, s.PurgeDeletion(ctx, scope, key), port.ErrPurgeNotReady)
		require.NoError(t, db.Model(&adapter.ResourceDeletion{}).Where("resource_id = ?", key).Update("purge_after", time.Now().Add(-time.Hour)).Error)
		// Fail successive cleanup stages; each attempt must retain the entire
		// footprint and restore the bypass, even after earlier rows were deleted.
		failedTable := ""
		require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("test:purge_delete_failure", func(tx *gorm.DB) {
			if failedTable != "" && tx.Statement.Table == failedTable {
				tx.AddError(port.ErrInvalid)
			}
		}))
		for _, table := range []string{"roles", "users", "applications", "organizations", "tenants"} {
			failedTable = table
			require.Error(t, s.PurgeDeletion(ctx, scope, key), table)
			_, e := s.GetTenantByID(ctx, tid)
			require.NoError(t, e)
			_, e = s.GetApplication(ctx, app.ID())
			require.NoError(t, e)
			var bypass adapter.LifecycleControl
			require.NoError(t, db.First(&bypass, 1).Error)
			require.Zero(t, bypass.Bypass)
		}
		failedTable = ""
		// Inject a late failure: every table deletion and bypass must roll back.
		fail := true
		require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:purge_failure", func(tx *gorm.DB) {
			if fail && tx.Statement.Table == "mutation_audits" {
				tx.AddError(port.ErrInvalid)
			}
		}))
		require.Error(t, s.PurgeDeletion(ctx, scope, key))
		_, err = s.GetTenantByID(ctx, tid)
		require.NoError(t, err)
		_, err = s.GetApplication(ctx, app.ID())
		require.NoError(t, err)
		status, err := s.ReadDeletion(ctx, scope, key)
		require.NoError(t, err)
		require.Equal(t, "purge_failed", status.Diagnostic)
		fail = false
		require.NoError(t, s.PurgeDeletion(ctx, scope, key))
		require.NoError(t, s.PurgeDeletion(ctx, scope, key))
		_, err = s.GetTenantByID(ctx, tid)
		require.ErrorIs(t, err, port.ErrNotFound)
		_, err = s.GetApplication(ctx, app.ID())
		require.ErrorIs(t, err, port.ErrNotFound)
		_, err = s.GetTenantByID(ctx, other)
		require.NoError(t, err)
		status, err = s.ReadDeletion(ctx, scope, key)
		require.NoError(t, err)
		require.NotNil(t, status.PurgedAt)
		var control adapter.LifecycleControl
		require.NoError(t, db.First(&control, 1).Error)
		require.Zero(t, control.Bypass)
		var publications []adapter.Publication
		require.NoError(t, db.Find(&publications).Error)
		for _, publication := range publications {
			var event model.CommonEvent
			require.NoError(t, json.Unmarshal([]byte(publication.Payload), &event))
			require.Regexp(t, "^[0-9a-f]{32}$", event.RequestID)
		}
	})
}

func TestLifecycleLeafRecreationAndLastOwner(t *testing.T) {
	eachBackend(t, func(t *testing.T, s *adapter.Store) {
		ctx := t.Context()
		tenant, err := s.GetTenantBySlug(ctx, model.DefaultTenantSlug)
		require.NoError(t, err)
		tid := tenant.ID()
		api := service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))
		scope := model.CommonScope{Family: "tenant_domain", TenantID: string(tid)}
		require.NoError(t, s.DeleteCommonLeaf(ctx, scope, "gone.example", model.MatchCondition{}))
		require.ErrorIs(t, s.DeleteCommonLeaf(ctx, scope, "gone.example", model.MatchCondition{Present: true, Any: true}), port.ErrPreconditionFailed)
		_, err = api.PutCommonDomain(ctx, tid, "gone.example", "active")
		require.NoError(t, err)
		old, err := s.ReadCommon(ctx, scope, "gone.example")
		require.NoError(t, err)
		c, err := model.ParseMatchCondition([]string{old.ETag})
		require.NoError(t, err)
		require.NoError(t, s.DeleteCommonLeaf(ctx, scope, "gone.example", c))
		_, err = api.PutCommonDomain(ctx, tid, "gone.example", "active")
		require.NoError(t, err)
		require.ErrorIs(t, s.DeleteCommonLeaf(ctx, scope, "gone.example", c), port.ErrPreconditionFailed)
		uid := model.NewUserID()
		_, err = api.PutCommonMember(ctx, tid, uid, service.CommonMember{Email: "last@test.local", TenantRole: "owner", Status: "active"})
		require.NoError(t, err)
		_, err = s.ScheduleDeletion(ctx, model.CommonScope{Family: "member", TenantID: string(tid)}, string(uid), model.MatchCondition{}, time.Second)
		require.ErrorIs(t, err, port.ErrLastOwner)
	})
}

// A recovery fixture that removes the publication clock must also remove the
// later lifecycle triggers: neither existed in the simulated older release.
func removeLifecycleTestGuards(t *testing.T, db *gorm.DB) {
	t.Helper()
	if db.Dialector.Name() == "postgres" {
		var rows []struct {
			Name      string
			TableName string
		}
		require.NoError(t, db.Raw("SELECT t.tgname AS name, c.relname AS table_name FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND t.tgname LIKE 'xolo_lifecycle_%'").Scan(&rows).Error)
		for _, r := range rows {
			require.NoError(t, db.Exec("DROP TRIGGER "+db.Statement.Quote(r.Name)+" ON "+db.Statement.Quote(r.TableName)).Error)
		}
	} else {
		var rows []struct{ Name string }
		require.NoError(t, db.Raw("SELECT name FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'xolo_lifecycle_%'").Scan(&rows).Error)
		for _, r := range rows {
			require.NoError(t, db.Exec("DROP TRIGGER "+db.Statement.Quote(r.Name)).Error)
		}
	}
}

func TestLifecycleMembershipRecreationAndDisabledLocalDelete(t *testing.T) {
	eachBackend(t, func(t *testing.T, s *adapter.Store) {
		ctx := t.Context()
		api := service.NewProvisioningService(s, s, s, s, service.WithMultiTenant(true))
		org := model.NewOrganization(testTenantID, "leaves", "Leaves", "")
		require.NoError(t, s.CreateOrg(ctx, org))
		require.ErrorIs(t, s.DeleteOrg(ctx, org.ID()), port.ErrLifecycleDisabled)
		users := []*model.BaseUser{
			model.NewUser(testTenantID, "local", "owner", "owner@test", "Owner", true),
			model.NewUser(testTenantID, "local", "other", "other@test", "Other", true),
		}
		for _, u := range users {
			require.NoError(t, s.SaveUser(ctx, u))
		}
		require.NoError(t, s.SetCommonMembership(ctx, org.ID(), users[0].ID(), model.MembershipRoleOwner, model.StatusActive))
		require.NoError(t, s.SetCommonMembership(ctx, org.ID(), users[1].ID(), model.MembershipRoleMember, model.StatusActive))
		scope := model.CommonScope{Family: "organization_membership", TenantID: string(testTenantID), OrganizationID: string(org.ID())}
		require.ErrorIs(t, s.DeleteCommonLeaf(ctx, scope, string(users[0].ID()), model.MatchCondition{}), port.ErrLastOwner)
		key := string(users[1].ID())
		old, err := s.ReadCommon(ctx, scope, key)
		require.NoError(t, err)
		c, err := model.ParseMatchCondition([]string{old.ETag})
		require.NoError(t, err)
		require.NoError(t, s.DeleteCommonLeaf(ctx, scope, key, c))
		require.NoError(t, s.DeleteCommonLeaf(ctx, scope, key, model.MatchCondition{}))
		require.ErrorIs(t, s.DeleteCommonLeaf(ctx, scope, key, c), port.ErrPreconditionFailed)
		_, err = api.PutCommonMembership(ctx, testTenantID, org.ID(), users[1].ID(), service.CommonMembership{Role: "member", Status: "active"})
		require.NoError(t, err)
		recreated, err := s.ReadCommon(ctx, scope, key)
		require.NoError(t, err)
		require.NotEqual(t, old.ETag, recreated.ETag)
		require.ErrorIs(t, s.DeleteCommonLeaf(ctx, scope, key, c), port.ErrPreconditionFailed)
		foreign := scope
		foreign.TenantID = string(model.NewTenantID())
		require.ErrorIs(t, s.DeleteCommonLeaf(ctx, foreign, key, model.MatchCondition{}), port.ErrParentNotFound)
	})
}
