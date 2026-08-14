package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// ── Fakes ─────────────────────────────────────────────────────────────────────

type fakeQuotaStore struct {
	userQuota model.Quota
	orgQuota  model.Quota
}

func (f *fakeQuotaStore) SetQuota(_ context.Context, _ model.Quota) error { return nil }

func (f *fakeQuotaStore) GetQuota(_ context.Context, scope model.QuotaScope, _ string) (model.Quota, error) {
	switch scope {
	case model.QuotaScopeUser:
		if f.userQuota == nil {
			return nil, port.ErrNotFound
		}
		return f.userQuota, nil
	case model.QuotaScopeOrg:
		if f.orgQuota == nil {
			return nil, port.ErrNotFound
		}
		return f.orgQuota, nil
	}
	return nil, port.ErrNotFound
}

func (f *fakeQuotaStore) ResolveEffectiveQuota(_ context.Context, _ model.UserID, _ model.OrgID) (*model.EffectiveQuota, error) {
	return nil, nil // not used by QuotaService
}

func (f *fakeQuotaStore) ResolveEffectiveQuotaForApplication(_ context.Context, _ model.ApplicationID, _ model.OrgID) (*model.EffectiveQuota, error) {
	return nil, nil
}

// fakeOrgProvider satisfies service.OrgProvider (narrow interface used by QuotaService).
type fakeOrgProvider struct {
	org     model.Organization
	members []model.Membership
	listErr error
}

func (f *fakeOrgProvider) GetOrgByID(_ context.Context, _ model.OrgID) (model.Organization, error) {
	return f.org, nil
}
func (f *fakeOrgProvider) ListOrgMembers(_ context.Context, _ model.OrgID, _ port.ListOrgMembersOptions) ([]model.Membership, int64, error) {
	return f.members, int64(len(f.members)), f.listErr
}

// fakeMembership satisfies model.Membership minimally.
type fakeMembership struct{}

func (m *fakeMembership) ID() model.MembershipID  { return "" }
func (m *fakeMembership) OrgID() model.OrgID      { return "" }
func (m *fakeMembership) UserID() model.UserID    { return "" }
func (m *fakeMembership) CreatedAt() time.Time    { return time.Time{} }
func (m *fakeMembership) User() model.User        { return nil }
func (m *fakeMembership) Org() model.Organization { return nil }
func (m *fakeMembership) Roles() []model.Role     { return nil }

// ptr is a convenience helper.
func ptr[T any](v T) *T { return &v }

// fakeOrg implements model.Organization with ShareQuotaEqually support.
type fakeOrg struct {
	shareQuotaEqually bool
}

func (o *fakeOrg) ID() model.OrgID          { return "org1" }
func (o *fakeOrg) TenantID() model.TenantID { return "tenant1" }
func (o *fakeOrg) Slug() string             { return "org1" }
func (o *fakeOrg) Name() string             { return "Org1" }
func (o *fakeOrg) Description() string      { return "" }
func (o *fakeOrg) Active() bool             { return true }
func (o *fakeOrg) Currency() string         { return "EUR" }
func (o *fakeOrg) CreatedAt() time.Time     { return time.Time{} }
func (o *fakeOrg) UpdatedAt() time.Time     { return time.Time{} }
func (o *fakeOrg) ShareQuotaEqually() bool  { return o.shareQuotaEqually }

// fakeQuota satisfies model.Quota.
type fakeQuota struct {
	daily, monthly, yearly *int64
	currency               string
	scope                  model.QuotaScope
	scopeID                string
}

func (q *fakeQuota) ID() model.QuotaID       { return "" }
func (q *fakeQuota) Scope() model.QuotaScope { return q.scope }
func (q *fakeQuota) ScopeID() string         { return q.scopeID }
func (q *fakeQuota) Currency() string        { return q.currency }
func (q *fakeQuota) DailyBudget() *int64     { return q.daily }
func (q *fakeQuota) MonthlyBudget() *int64   { return q.monthly }
func (q *fakeQuota) YearlyBudget() *int64    { return q.yearly }
func (q *fakeQuota) CreatedAt() time.Time    { return time.Time{} }
func (q *fakeQuota) UpdatedAt() time.Time    { return time.Time{} }

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestQuotaService_ResolveEffectiveQuota_NoSharing: flag désactivé → min-merge préservé.
func TestQuotaService_ResolveEffectiveQuota_NoSharing(t *testing.T) {
	orgQuota := &fakeQuota{daily: ptr[int64](6_000_000), currency: "EUR", scope: model.QuotaScopeOrg, scopeID: "org1"}
	userQuota := &fakeQuota{daily: ptr[int64](2_000_000), currency: "EUR", scope: model.QuotaScopeUser, scopeID: "user1"}

	svc := service.NewQuotaService(
		&fakeQuotaStore{orgQuota: orgQuota, userQuota: userQuota},
		&fakeOrgProvider{org: &fakeOrg{shareQuotaEqually: false}},
	)

	got, err := svc.ResolveEffectiveQuota(context.Background(), "user1", "org1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DailyBudget == nil || *got.DailyBudget != 2_000_000 {
		t.Errorf("expected min daily budget 2_000_000, got %v", got.DailyBudget)
	}
}

// TestQuotaService_ResolveEffectiveQuota_SharingEnabled_NoUserQuota: quota distribué = orgBudget/N.
func TestQuotaService_ResolveEffectiveQuota_SharingEnabled_NoUserQuota(t *testing.T) {
	orgQuota := &fakeQuota{daily: ptr[int64](6_000_000), monthly: ptr[int64](60_000_000), currency: "EUR", scope: model.QuotaScopeOrg, scopeID: "org1"}
	members := []model.Membership{&fakeMembership{}, &fakeMembership{}, &fakeMembership{}} // 3 members

	svc := service.NewQuotaService(
		&fakeQuotaStore{orgQuota: orgQuota},
		&fakeOrgProvider{org: &fakeOrg{shareQuotaEqually: true}, members: members},
	)

	got, err := svc.ResolveEffectiveQuota(context.Background(), "user1", "org1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DailyBudget == nil || *got.DailyBudget != 2_000_000 {
		t.Errorf("expected daily 2_000_000 (6M/3), got %v", got.DailyBudget)
	}
	if got.MonthlyBudget == nil || *got.MonthlyBudget != 20_000_000 {
		t.Errorf("expected monthly 20_000_000 (60M/3), got %v", got.MonthlyBudget)
	}
	if got.YearlyBudget != nil {
		t.Errorf("expected nil yearly budget, got %v", got.YearlyBudget)
	}
}

// TestQuotaService_ResolveEffectiveQuota_SharingEnabled_WithUserQuota: quota perso → sharing ignoré.
func TestQuotaService_ResolveEffectiveQuota_SharingEnabled_WithUserQuota(t *testing.T) {
	orgQuota := &fakeQuota{daily: ptr[int64](6_000_000), currency: "EUR", scope: model.QuotaScopeOrg, scopeID: "org1"}
	userQuota := &fakeQuota{daily: ptr[int64](1_000_000), currency: "EUR", scope: model.QuotaScopeUser, scopeID: "user1"}
	members := []model.Membership{&fakeMembership{}, &fakeMembership{}, &fakeMembership{}}

	svc := service.NewQuotaService(
		&fakeQuotaStore{orgQuota: orgQuota, userQuota: userQuota},
		&fakeOrgProvider{org: &fakeOrg{shareQuotaEqually: true}, members: members},
	)

	got, err := svc.ResolveEffectiveQuota(context.Background(), "user1", "org1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With userQuota set, sharing is ignored → min-merge: min(1M, 6M) = 1M
	if got.DailyBudget == nil || *got.DailyBudget != 1_000_000 {
		t.Errorf("expected daily 1_000_000 (min-merge), got %v", got.DailyBudget)
	}
}

// TestQuotaService_ResolveEffectiveQuota_SharingEnabled_ZeroMembers: n=0 → unlimited.
func TestQuotaService_ResolveEffectiveQuota_SharingEnabled_ZeroMembers(t *testing.T) {
	orgQuota := &fakeQuota{daily: ptr[int64](6_000_000), currency: "EUR", scope: model.QuotaScopeOrg, scopeID: "org1"}

	svc := service.NewQuotaService(
		&fakeQuotaStore{orgQuota: orgQuota},
		&fakeOrgProvider{org: &fakeOrg{shareQuotaEqually: true}, members: nil},
	)

	got, err := svc.ResolveEffectiveQuota(context.Background(), "user1", "org1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DailyBudget != nil {
		t.Errorf("expected nil daily budget (unlimited), got %v", got.DailyBudget)
	}
}

// TestQuotaService_ResolveEffectiveQuota_ListMembersError: erreur propagée.
func TestQuotaService_ResolveEffectiveQuota_ListMembersError(t *testing.T) {
	orgQuota := &fakeQuota{daily: ptr[int64](6_000_000), currency: "EUR", scope: model.QuotaScopeOrg, scopeID: "org1"}
	listErr := errors.New("db error")

	svc := service.NewQuotaService(
		&fakeQuotaStore{orgQuota: orgQuota},
		&fakeOrgProvider{org: &fakeOrg{shareQuotaEqually: true}, listErr: listErr},
	)

	_, err := svc.ResolveEffectiveQuota(context.Background(), "user1", "org1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
