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
	appQuota  model.Quota
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
	case model.QuotaScopeApplication:
		if f.appQuota == nil {
			return nil, port.ErrNotFound
		}
		return f.appQuota, nil
	}
	return nil, port.ErrNotFound
}

func (f *fakeQuotaStore) ResolveEffectiveQuota(_ context.Context, _ model.UserID, _ model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	return nil, f.orgQuota, nil // not used by QuotaService
}

func (f *fakeQuotaStore) ResolveEffectiveQuotaForApplication(_ context.Context, _ model.ApplicationID, _ model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	return nil, f.orgQuota, nil
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

	got, _, err := svc.ResolveEffectiveQuota(context.Background(), "user1", "org1")
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

	got, returnedOrgQuota, err := svc.ResolveEffectiveQuota(context.Background(), "user1", "org1")
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
	// Issue #82: the sharing branch synthesises the per-user budget from the
	// raw org quota (org/N), but the second return value MUST still be the
	// raw org quota — the org-wide block in the enforcer depends on it. A
	// regression where the sharing branch returns nil here would silently
	// turn the org cap off for every user in a sharing-enabled organisation,
	// and that bug class is exactly what this PR is meant to close.
	if returnedOrgQuota != orgQuota {
		t.Errorf("expected the second return value to be the raw orgQuota, got %+v", returnedOrgQuota)
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

	got, _, err := svc.ResolveEffectiveQuota(context.Background(), "user1", "org1")
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

	got, returnedOrgQuota, err := svc.ResolveEffectiveQuota(context.Background(), "user1", "org1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DailyBudget != nil {
		t.Errorf("expected nil daily budget (unlimited), got %v", got.DailyBudget)
	}
	// Issue #82: the n==0 sharing branch must still surface the raw org
	// quota so the enforcer's org-wide block keeps enforcing the cap on this
	// path. Without this assertion a future refactor could drop the second
	// return on the n==0 path without any test noticing.
	if returnedOrgQuota != orgQuota {
		t.Errorf("expected the second return value to be the raw orgQuota, got %+v", returnedOrgQuota)
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

	_, _, err := svc.ResolveEffectiveQuota(context.Background(), "user1", "org1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestQuotaService_ResolveEffectiveQuotaForApplication_MinMerge: le quota effectif
// d'une application est le min du quota application et du quota d'org, sur
// chaque période. Le partage de l'org par membre ne s'applique pas : un token
// d'application n'est pas un membre et l'opérateur qui pose un budget sur
// l'application veut que ce budget s'applique tel quel.
func TestQuotaService_ResolveEffectiveQuotaForApplication_MinMerge(t *testing.T) {
	orgQuota := &fakeQuota{daily: ptr[int64](6_000_000), monthly: ptr[int64](60_000_000), currency: "EUR", scope: model.QuotaScopeOrg, scopeID: "org1"}
	appQuota := &fakeQuota{daily: ptr[int64](60_000), monthly: ptr[int64](1_500_000), yearly: ptr[int64](20_000_000), currency: "EUR", scope: model.QuotaScopeApplication, scopeID: "app-1"}

	svc := service.NewQuotaService(
		&fakeQuotaStore{orgQuota: orgQuota, appQuota: appQuota},
		&fakeOrgProvider{org: &fakeOrg{shareQuotaEqually: true}, members: []model.Membership{&fakeMembership{}, &fakeMembership{}, &fakeMembership{}}},
	)

	got, _, err := svc.ResolveEffectiveQuotaForApplication(context.Background(), "app-1", "org1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DailyBudget == nil || *got.DailyBudget != 60_000 {
		t.Errorf("daily = %v, want 60_000 (the application budget, stricter than the org)", got.DailyBudget)
	}
	if got.MonthlyBudget == nil || *got.MonthlyBudget != 1_500_000 {
		t.Errorf("monthly = %v, want 1_500_000", got.MonthlyBudget)
	}
	if got.YearlyBudget == nil || *got.YearlyBudget != 20_000_000 {
		t.Errorf("yearly = %v, want 20_000_000 (only set on the application)", got.YearlyBudget)
	}
}

// TestQuotaService_ResolveEffectiveQuotaForApplication_OrgOnly: sans budget
// application, le quota effectif retombe sur le quota d'org, comme pour un
// utilisateur qui n'a pas de quota personnel.
func TestQuotaService_ResolveEffectiveQuotaForApplication_OrgOnly(t *testing.T) {
	orgQuota := &fakeQuota{daily: ptr[int64](6_000_000), currency: "EUR", scope: model.QuotaScopeOrg, scopeID: "org1"}

	svc := service.NewQuotaService(
		&fakeQuotaStore{orgQuota: orgQuota},
		&fakeOrgProvider{org: &fakeOrg{shareQuotaEqually: true}, members: []model.Membership{&fakeMembership{}, &fakeMembership{}}},
	)

	got, _, err := svc.ResolveEffectiveQuotaForApplication(context.Background(), "app-1", "org1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DailyBudget == nil || *got.DailyBudget != 6_000_000 {
		t.Errorf("daily = %v, want 6_000_000 (org only)", got.DailyBudget)
	}
}

// TestQuotaService_ResolveEffectiveQuotaForApplication_NothingSet: sans budget
// nulle part, le résultat est un EffectiveQuota vide (tout nil, devise par
// défaut) — un tel quota ne déclenche aucun plafond.
func TestQuotaService_ResolveEffectiveQuotaForApplication_NothingSet(t *testing.T) {
	svc := service.NewQuotaService(
		&fakeQuotaStore{},
		&fakeOrgProvider{org: &fakeOrg{}},
	)

	got, _, err := svc.ResolveEffectiveQuotaForApplication(context.Background(), "app-1", "org1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DailyBudget != nil || got.MonthlyBudget != nil || got.YearlyBudget != nil {
		t.Errorf("expected all-nil budget, got %+v", got)
	}
	if got.Currency != model.DefaultCurrency {
		t.Errorf("currency = %q, want default %q", got.Currency, model.DefaultCurrency)
	}
}

// TestQuotaService_ResolveEffectiveQuotaForApplication_OrgTakesPrecedenceOnCurrency:
// quand un quota d'org existe, sa devise est retenue même si l'application en
// a une autre : les budgets posés sur l'application sont en devise d'org dans
// tous les cas, pour éviter de mélanger les unités au moment du min-merge.
func TestQuotaService_ResolveEffectiveQuotaForApplication_OrgTakesPrecedenceOnCurrency(t *testing.T) {
	orgQuota := &fakeQuota{daily: ptr[int64](6_000_000), currency: "EUR", scope: model.QuotaScopeOrg, scopeID: "org1"}
	appQuota := &fakeQuota{daily: ptr[int64](60_000), currency: "USD", scope: model.QuotaScopeApplication, scopeID: "app-1"}

	svc := service.NewQuotaService(
		&fakeQuotaStore{orgQuota: orgQuota, appQuota: appQuota},
		&fakeOrgProvider{org: &fakeOrg{}},
	)

	got, _, err := svc.ResolveEffectiveQuotaForApplication(context.Background(), "app-1", "org1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Currency != "EUR" {
		t.Errorf("currency = %q, want EUR (org takes precedence)", got.Currency)
	}
}
