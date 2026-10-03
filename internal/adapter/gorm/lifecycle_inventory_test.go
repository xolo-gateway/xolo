package gorm_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

func TestLifecyclePersonalInventoryAndDeliveryCopies(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		s, tenant, _ := webhookFixture(t, db)
		ctx := t.Context()
		org := model.NewOrganization(tenant.ID(), "personal", "Personal", "")
		require.NoError(t, s.CreateOrg(ctx, org))
		user := model.NewUser(tenant.ID(), "local", "member", "member@test", "Member", true)
		keeper := model.NewUser(tenant.ID(), "local", "keeper", "keeper@test", "Keeper", true)
		require.NoError(t, s.SaveUser(ctx, user))
		require.NoError(t, s.SaveUser(ctx, keeper))
		require.NoError(t, s.SetCommonMembership(ctx, org.ID(), user.ID(), "member", "active"))
		require.NoError(t, s.SetCommonMembership(ctx, org.ID(), keeper.ID(), "owner", "active"))
		uid, oid := string(user.ID()), string(org.ID())
		email := user.Email()
		historicalInvite := model.NewInviteToken(org.ID(), "member", &email, nil, nil, keeper.ID())
		require.NoError(t, s.CreateInvite(ctx, historicalInvite))
		require.NoError(t, s.DeleteInvite(ctx, historicalInvite.ID()))
		attrs := map[string]string{"member_user_id": uid}
		alert := model.NewAlert(org.ID(), user.ID(), "personal", model.WithAlertScope(model.AlertScopePersonal))
		require.NoError(t, s.CreateAlert(ctx, alert))
		retained := model.NewAlert(org.ID(), user.ID(), "retained", model.WithAlertScope(model.AlertScopeOrg))
		require.NoError(t, s.CreateAlert(ctx, retained))
		for _, row := range []any{
			&adapter.PersonalVirtualModel{ID: "personal-vm", UserID: uid, Name: "Personal"},
			&adapter.PluginNodeSecret{ID: "personal-secret", OrgID: "~:" + uid, PluginName: "test", NodeID: "n", Key: "k", ValueEncrypted: "encrypted"},
			&adapter.InviteToken{ID: "addressed-invite", OrgID: oid, CreatedByUserID: string(keeper.ID()), InviteeEmail: &email, Role: "member"},
			&adapter.UsageRecord{ID: "personal-usage", OrgID: oid, UserID: uid},
			&adapter.QuotaUsage{Scope: "user", ScopeID: uid, OrgID: oid, Currency: "USD", Day: "2026-10-03", Cost: 1},
			&adapter.Event{ID: "member-fact", OrgID: oid, Attributes: adapter.JSONColumn[map[string]string]{Val: &attrs}},
			&adapter.AlertIncident{ID: "personal-incident", OrgID: oid, AlertID: string(alert.ID())},
		} {
			require.NoError(t, db.Create(row).Error)
		}
		require.NoError(t, s.PrepareWebhooks(ctx, 100))
		var copies int64
		require.NoError(t, db.Model(&adapter.WebhookDelivery{}).Count(&copies).Error)
		require.Positive(t, copies)
		// Source retention must not hide already materialized copies from erasure.
		require.NoError(t, s.PurgeCommonEvents(ctx, time.Now().Add(time.Hour)))
		scope := model.CommonScope{Family: "member", TenantID: string(tenant.ID())}
		d, err := s.ScheduleDeletion(ctx, scope, uid, model.MatchCondition{}, time.Second)
		require.NoError(t, err)
		require.ErrorContains(t, db.Create(&adapter.InviteToken{ID: "late-invite", OrgID: oid, CreatedByUserID: string(keeper.ID()), InviteeEmail: &email, Role: "member"}).Error, "xolo_resource_deleted")
		raw, err := s.ExportDeletion(ctx, scope, uid)
		require.NoError(t, err)
		var archive struct {
			SHA256  string
			Payload struct{ Tables map[string][]map[string]any }
		}
		require.NoError(t, json.Unmarshal(raw, &archive))
		for _, table := range []string{"users", "personal_virtual_models", "plugin_node_secrets", "invite_tokens", "usage_records", "quota_usages", "alert_incidents", "alerts"} {
			require.Len(t, archive.Payload.Tables[table], 1, table)
		}
		require.NotEmpty(t, archive.Payload.Tables["webhook_deliveries"])
		require.NotEmpty(t, archive.Payload.Tables["events"])
		historicalFacts := 0
		for _, row := range archive.Payload.Tables["mutation_audits"] {
			if row["resource_id"] == string(historicalInvite.ID()) {
				historicalFacts++
			}
		}
		require.Equal(t, 2, historicalFacts, "already removed invitations still own private audit snapshots")
		c, err := model.ParseMatchCondition([]string{d.ETag})
		require.NoError(t, err)
		_, err = s.ConfirmDeletion(ctx, scope, uid, c, archive.SHA256)
		require.NoError(t, err)
		require.NoError(t, db.Model(&adapter.ResourceDeletion{}).Where("resource_id = ?", uid).Update("purge_after", time.Now().Add(-time.Hour)).Error)
		require.NoError(t, s.PurgeDeletion(ctx, scope, uid))
		for _, table := range []string{"personal_virtual_models", "plugin_node_secrets", "invite_tokens", "usage_records", "quota_usages", "alert_incidents"} {
			var n int64
			require.NoError(t, db.Table(table).Count(&n).Error)
			require.Zero(t, n, table)
		}
		_, err = s.GetAlertByID(ctx, alert.ID())
		require.ErrorIs(t, err, port.ErrNotFound)
		item, err := s.ReadCommon(ctx, model.CommonScope{Family: "alert", TenantID: string(tenant.ID()), OrganizationID: oid}, string(retained.ID()))
		require.NoError(t, err)
		require.Contains(t, string(item.Representation), `"owner_id":""`)
		_, err = s.GetUserByID(ctx, keeper.ID())
		require.NoError(t, err)
		_, err = s.GetOrgByID(ctx, org.ID())
		require.NoError(t, err)
		var remaining int64
		require.NoError(t, db.Model(&adapter.MutationAudit{}).Where("resource_id = ?", string(historicalInvite.ID())).Count(&remaining).Error)
		require.Zero(t, remaining)
		var deliveries []adapter.WebhookDelivery
		require.NoError(t, db.Find(&deliveries).Error)
		for _, delivery := range deliveries {
			require.NotContains(t, delivery.Body, uid)
		}
		var facts []adapter.Event
		require.NoError(t, db.Find(&facts).Error)
		for _, fact := range facts {
			require.NotEqual(t, "member-fact", fact.ID)
			if fact.Attributes.Val != nil {
				require.NotEqual(t, email, (*fact.Attributes.Val)["email"])
			}
		}
	})
}

func TestLifecycleParentPriorityAndWrongScopeReceipt(t *testing.T) {
	eachBackendDB(t, func(t *testing.T, db *gorm.DB) {
		ctx := t.Context()
		s := adapter.NewStore(db)
		require.NoError(t, s.Migrate(ctx))
		tenant := model.NewTenant("retire", "Retire", "")
		require.NoError(t, s.CreateTenant(ctx, tenant))
		org := model.NewOrganization(tenant.ID(), "child", "Child", "")
		require.NoError(t, s.CreateOrg(ctx, org))
		childScope := model.CommonScope{Family: "organization", TenantID: string(tenant.ID())}
		child, err := s.ScheduleDeletion(ctx, childScope, string(org.ID()), model.MatchCondition{}, time.Second)
		require.NoError(t, err)
		raw, err := s.ExportDeletion(ctx, childScope, string(org.ID()))
		require.NoError(t, err)
		var archive struct{ SHA256 string }
		require.NoError(t, json.Unmarshal(raw, &archive))
		c, err := model.ParseMatchCondition([]string{child.ETag})
		require.NoError(t, err)
		_, err = s.ConfirmDeletion(ctx, childScope, string(org.ID()), c, archive.SHA256)
		require.NoError(t, err)
		parentScope := model.CommonScope{Family: "tenant"}
		parent, err := s.ScheduleDeletion(ctx, parentScope, string(tenant.ID()), model.MatchCondition{}, time.Second)
		require.NoError(t, err)
		pc, err := model.ParseMatchCondition([]string{parent.ETag})
		require.NoError(t, err)
		_, err = s.ConfirmDeletion(ctx, parentScope, string(tenant.ID()), pc, archive.SHA256)
		require.ErrorIs(t, err, port.ErrExportMismatch)
		require.NoError(t, db.Model(&adapter.ResourceDeletion{}).Where("tenant_id = ?", string(tenant.ID())).Update("purge_after", time.Now().Add(-time.Hour)).Error)
		require.ErrorIs(t, s.PurgeDeletion(ctx, childScope, string(org.ID())), port.ErrPurgeNotReady)
		due, err := s.DueDeletions(ctx, 100)
		require.NoError(t, err)
		require.Empty(t, due)
		raw, err = s.ExportDeletion(ctx, parentScope, string(tenant.ID()))
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &archive))
		_, err = s.ConfirmDeletion(ctx, parentScope, string(tenant.ID()), pc, archive.SHA256)
		require.NoError(t, err)
		// A new service instance resumes entirely from persisted eligibility.
		restarted := adapter.NewStore(db)
		require.NoError(t, restarted.Migrate(ctx))
		require.NoError(t, restarted.PurgeDeletion(ctx, parentScope, string(tenant.ID())))
		child, err = s.ReadDeletion(ctx, childScope, string(org.ID()))
		require.NoError(t, err)
		require.NotNil(t, child.PurgedAt)
		require.NoError(t, s.PurgeDeletion(ctx, childScope, string(org.ID())))
		var n int64
		require.NoError(t, db.Model(&adapter.DeletionArchive{}).Count(&n).Error)
		require.Zero(t, n)
	})
}
