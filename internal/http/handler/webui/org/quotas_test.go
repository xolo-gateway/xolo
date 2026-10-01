package org

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// stubQuotaStore for the application-quota handler tests. Every method
// returns ErrNotFound unless the test sets the corresponding field, which
// lets each table-driven case pin one branch without constructing a fully
// populated store. quotaScope filters GetQuota by scope (Quotas are scoped,
// so a stub seeded with an application quota would otherwise also match
// when a handler asks for the org quota of the same id).
type stubQuotaStore struct {
	port.QuotaStore
	quota      model.Quota
	quotaScope model.QuotaScope // scope GetQuota responds to; zero value = match any scope
	err        error
}

func (s *stubQuotaStore) GetQuota(_ context.Context, scope model.QuotaScope, _ string) (model.Quota, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.quota != nil && (s.quotaScope == "" || s.quotaScope == scope) {
		return s.quota, nil
	}
	return nil, port.ErrNotFound
}

func (s *stubQuotaStore) SetQuota(_ context.Context, q model.Quota) error {
	if s.err != nil {
		return s.err
	}
	s.quota = q
	s.quotaScope = q.Scope()
	return nil
}

func (s *stubQuotaStore) ResolveEffectiveQuota(context.Context, model.UserID, model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	return &model.EffectiveQuota{}, nil, nil
}

func (s *stubQuotaStore) ResolveEffectiveQuotaForApplication(context.Context, model.ApplicationID, model.OrgID) (*model.EffectiveQuota, model.Quota, error) {
	return &model.EffectiveQuota{}, nil, nil
}

// stubUsageStore for the application-quota handler tests: every read returns
// zero unless the test sets it. The application budget editor only reads
// (no writes happen through this store on GET), so the failure modes are
// "ok with zero" and "non-ErrNotFound error".
type stubUsageStore struct {
	port.UsageStore
	err error
}

func (s *stubUsageStore) SumQuotaCostSince(_ context.Context, _ model.QuotaScope, _ string, _ model.OrgID, _ time.Time) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return 0, nil
}

func (s *stubUsageStore) SumCostSinceByCurrency(context.Context, []model.UserID, model.OrgID, time.Time) (map[string]int64, error) {
	return map[string]int64{}, nil
}

// handlerWithStubs wires the bare minimum needed by the application quota
// handlers: a Handler with the org/application/quota/usage stores replaced
// by stubs, and a tenant id in context so resolveOrgAndApplication finds
// the org.
func handlerWithStubs(org model.Organization, app model.Application, qStore *stubQuotaStore, uStore *stubUsageStore) *Handler {
	return &Handler{
		orgStore:         &stubOrgStore{org: org},
		applicationStore: &stubApplicationStore{app: app},
		quotaStore:       qStore,
		usageStore:       uStore,
	}
}

func newRequest(orgSlug, appID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/orgs/"+orgSlug+"/admin/applications/"+appID+"/quota", nil)
	r.SetPathValue("orgSlug", orgSlug)
	r.SetPathValue("appID", appID)
	// resolveOrgAndApplication reads TenantID from the context, and the
	// templ component reads BaseURL from it. Set both: a minimal valid URL
	// so BaseURLString does not panic when rendering the page.
	base, _ := url.Parse("http://example.test")
	r = r.WithContext(httpCtx.SetBaseURL(r.Context(), base.String()))
	return r
}

// renderHandler runs a handler and returns the rendered HTML body so the
// tests can assert on visible strings without depending on templ's internals.
func renderHandler(h http.HandlerFunc, r *http.Request) string {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Body.String()
}

// TestGetApplicationQuotaPage covers the four GET branches: no budget row
// (renders empty), budget populated, foreign appID (404), and store error
// other than ErrNotFound (banner rendered).
func TestGetApplicationQuotaPage(t *testing.T) {
	org := model.NewOrganization("tenant", "acme", "Acme", "EUR")
	daily := int64(60_000)
	app := model.NewApplication(org.ID(), "acme-app", "", true)

	tests := []struct {
		name        string
		store       *stubQuotaStore
		foreignApp  bool
		wantStatus  int
		wantInBody  []string // substrings that must appear in the rendered body
		wantNotBody []string // substrings that must NOT appear
	}{
		{
			// No budget row: form fields render empty; the dedicated editor
			// page does not show a "Aucun budget" line (the summary lives on
			// the application edit form, not here). The /quota page renders
			// the form with three blank inputs.
			name:       "no budget row renders empty fields",
			store:      &stubQuotaStore{},
			wantStatus: http.StatusOK,
			wantInBody: []string{
				"Budget de l&#39;application",
				"daily_budget",
				"monthly_budget",
				"yearly_budget",
				`placeholder="10.00"`,
			},
			wantNotBody: []string{
				"Budget indisponible",
			},
		},
		{
			name: "populated budget pre-fills the daily field",
			store: &stubQuotaStore{
				quota: model.NewQuota(model.QuotaScopeApplication, "acme-app", "EUR", &daily, nil, nil),
			},
			wantStatus: http.StatusOK,
			// formatBudgetField(60_000) → "0.06" (microcents / 1_000_000).
			// Pinning the rendered value catches a regression that drops the
			// stored budget on the GET path (the matching failure mode for
			// the re-render helper on POST, fixed together under #88).
			wantInBody: []string{
				"Budget de l&#39;application",
				"daily_budget",
				`value="0.06"`,
			},
			wantNotBody: []string{
				"Budget indisponible",
			},
		},
		{
			name:       "foreign appID answers 404",
			store:      &stubQuotaStore{},
			foreignApp: true,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "store error other than ErrNotFound renders the banner",
			store:      &stubQuotaStore{err: errors.New("backend down")},
			wantStatus: http.StatusOK,
			wantInBody: []string{
				"Budget indisponible",
			},
			wantNotBody: []string{
				`value="10.00"`, // no value populated since the quota could not be loaded
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			targetApp := app
			if tc.foreignApp {
				// An application that belongs to a different org.
				otherOrg := model.NewOrganization("tenant-other", "other", "Other", "EUR")
				targetApp = model.NewApplication(otherOrg.ID(), "other-app", "", true)
			}
			h := handlerWithStubs(org, app, tc.store, &stubUsageStore{})

			body := renderHandler(h.getApplicationQuotaPage, newRequest(org.Slug(), string(targetApp.ID())))

			if tc.wantStatus == http.StatusOK {
				lower := strings.ToLower(body)
				for _, want := range tc.wantInBody {
					if !strings.Contains(lower, strings.ToLower(want)) {
						t.Errorf("body missing %q; full body length=%d:\n%s", want, len(body), body)
					}
				}
				for _, dont := range tc.wantNotBody {
					if strings.Contains(lower, strings.ToLower(dont)) {
						t.Errorf("body contains %q (should not)", dont)
					}
				}
			} else {
				// Non-OK path: httptest.NewRecorder default status is 200,
				// so we look at the recorder instead.
				rec := httptest.NewRecorder()
				h.getApplicationQuotaPage(rec, newRequest(org.Slug(), string(targetApp.ID())))
				if rec.Code != tc.wantStatus {
					t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
				}
			}
		})
	}
}

// TestSaveApplicationQuotaHappyPath verifies the POST writes a QuotaScopeApplication
// row with the parsed fields, then redirects to the success URL.
func TestSaveApplicationQuotaHappyPath(t *testing.T) {
	org := model.NewOrganization("tenant", "acme", "Acme", "EUR")
	app := model.NewApplication(org.ID(), "acme-app", "", true)
	qStore := &stubQuotaStore{}

	h := handlerWithStubs(org, app, qStore, &stubUsageStore{})

	form := url.Values{}
	form.Set("daily_budget", "10")
	form.Set("monthly_budget", "100")
	form.Set("yearly_budget", "1000")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("orgSlug", org.Slug())
	r.SetPathValue("appID", string(app.ID()))

	rec := httptest.NewRecorder()
	h.saveApplicationQuota(rec, r)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	location := rec.Header().Get("Location")
	want := "/orgs/acme/admin/applications/" + string(app.ID()) + "/quota?success=saved"
	if location != want {
		t.Errorf("location = %q, want %q", location, want)
	}
	if qStore.quota == nil {
		t.Fatalf("SetQuota was not called")
	}
	if qStore.quota.Scope() != model.QuotaScopeApplication {
		t.Errorf("scope = %q, want application", qStore.quota.Scope())
	}
	if qStore.quota.ScopeID() != string(app.ID()) {
		t.Errorf("scope id = %q, want %q", qStore.quota.ScopeID(), string(app.ID()))
	}
}

// TestSaveApplicationQuotaForeignApp covers the cross-org POST leak: even
// when an attacker hand-crafts a form action pointing at an application
// belonging to another org, the handler refuses with 404.
func TestSaveApplicationQuotaForeignApp(t *testing.T) {
	org := model.NewOrganization("tenant", "acme", "Acme", "EUR")
	foreignOrg := model.NewOrganization("tenant-other", "other", "Other", "EUR")
	foreignApp := model.NewApplication(foreignOrg.ID(), "other-app", "", true)
	qStore := &stubQuotaStore{}

	h := handlerWithStubs(org, model.NewApplication(org.ID(), "acme-app", "", true), qStore, &stubUsageStore{})

	form := url.Values{}
	form.Set("daily_budget", "10")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("orgSlug", org.Slug())
	r.SetPathValue("appID", string(foreignApp.ID()))

	rec := httptest.NewRecorder()
	h.saveApplicationQuota(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if qStore.quota != nil {
		t.Errorf("SetQuota was called for a foreign application")
	}
}

// TestParseBudgetField covers every input parseBudgetField can see on the
// quota editor: empty (unlimited), the legitimate values, the edge of the
// supported range and every malformed input that issue #88 documents. A
// failure here is a regression: the previous parser silently mapped
// unparsable / negative / overflowing inputs to nil, which stored "no
// cap" and showed a green "saved" banner on top.
func TestParseBudgetField(t *testing.T) {
	// The form input is in whole currency units; the parser multiplies by
	// 1_000_000 to get microcents. The largest representable input is the
	// one that, scaled by 1_000_000, just fits in int64 — that is
	// math.MaxInt64 / 1_000_000, rounded down to the nearest whole unit.
	maxBudgetUnits := int64(math.MaxInt64 / 1_000_000) // 9_223_372_036_854
	mc := func(v int64) *int64 { return &v }

	tests := []struct {
		name          string
		input         string
		wantExact     *int64 // exact microcent value expected; nil asserts nothing about the pointer
		wantUnlimited bool   // true asserts the result must be nil (unlimited)
		wantErr       bool
	}{
		// Empty and well-formed values: the four outcomes the operator can
		// actually trigger through the form (without hand-crafting a POST).
		{name: "empty means unlimited", input: "", wantUnlimited: true},
		{name: "0 is a strict zero cap, not unlimited", input: "0", wantExact: mc(0)},
		{name: "whole number", input: "10", wantExact: mc(10_000_000)},
		{name: "decimal", input: "10.5", wantExact: mc(10_500_000)},
		{name: "leading decimal", input: "0.01", wantExact: mc(10_000)},
		{name: "trailing zero decimal", input: "10.00", wantExact: mc(10_000_000)},

		// Boundaries at the int64 conversion. The constant is rounded down
		// to the nearest whole unit, so the test value is at the edge of the
		// accepted range. The float64 conversion below loses a few units of
		// precision for values in the trillions, so the assertion compares
		// the expected *units* (which are still exact) rather than the
		// int64 microcent output.
		{name: "value at the integer cap is accepted", input: strconv.FormatInt(maxBudgetUnits, 10), wantExact: nil},
		{name: "value above the integer cap is rejected", input: strconv.FormatInt(maxBudgetUnits+1, 10), wantErr: true},
		{name: "scientific notation above the cap is rejected", input: "1e16", wantErr: true},
		{name: "scientific notation within int64 range is accepted", input: "1e9", wantExact: nil}, // 1e15 microcents, well within int64
		{name: "scientific notation close to int64 cap is accepted", input: "9e12", wantExact: nil},

		// Hand-crafted POSTs the previous parser also mishandled. These
		// are inputs that the form's type="number" cannot produce, but a
		// crafted POST can.
		{name: "negative is rejected", input: "-1", wantErr: true},
		{name: "NaN literal is rejected", input: "NaN", wantErr: true},
		{name: "positive infinity is rejected", input: "+Inf", wantErr: true},
		{name: "negative infinity is rejected", input: "-Inf", wantErr: true},
		{name: "Inf is rejected", input: "Inf", wantErr: true},
		{name: "unparsable text is rejected", input: "abc", wantErr: true},
		{name: "trailing garbage is rejected", input: "10abc", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBudgetField(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseBudgetField(%q) error = nil, want error", tc.input)
				}
				if got != nil {
					t.Errorf("parseBudgetField(%q) value = %v, want nil on error", tc.input, *got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBudgetField(%q) error = %v, want nil", tc.input, err)
			}
			if tc.wantUnlimited {
				if got != nil {
					t.Errorf("parseBudgetField(%q) = %v, want nil (unlimited)", tc.input, *got)
				}
				return
			}
			if tc.wantExact == nil {
				// The "wantExact = nil" cases above are values we accept
				// without pinning the exact microcent amount (float64
				// precision loses a few units in the trillions). Just
				// assert the parser accepted them and produced a non-nil
				// pointer.
				if got == nil {
					t.Errorf("parseBudgetField(%q) = nil, want non-nil", tc.input)
				}
				return
			}
			if got == nil {
				t.Errorf("parseBudgetField(%q) = nil, want %d", tc.input, *tc.wantExact)
				return
			}
			if *got != *tc.wantExact {
				t.Errorf("parseBudgetField(%q) = %d, want %d", tc.input, *got, *tc.wantExact)
			}
		})
	}
}

// TestSaveApplicationQuotaBadValueDoesNotOverwriteBudget is the regression
// test the ticket asks for: a hand-crafted POST with an invalid value must
// not silently replace an existing budget. The store is seeded with a row,
// the bad POST goes through, and the seeded row must still be intact (no
// SetQuota call, no rewrite) and the response must NOT carry a success
// redirect.
func TestSaveApplicationQuotaBadValueDoesNotOverwriteBudget(t *testing.T) {
	org := model.NewOrganization("tenant", "acme", "Acme", "EUR")
	app := model.NewApplication(org.ID(), "acme-app", "", true)

	// Seed the store with a budget the operator actually chose. A successful
	// save would point at &daily, so any rewrite would change *that* pointer
	// (the stub assigns a fresh Quota). Pinning it here proves the row is
	// untouched.
	daily := int64(5_000_000) // 5.00
	existing := model.NewQuota(model.QuotaScopeApplication, string(app.ID()), "EUR", &daily, nil, nil)
	qStore := &stubQuotaStore{quota: existing}

	h := handlerWithStubs(org, app, qStore, &stubUsageStore{})

	// Bad payload: the operator types 0 in daily (the bug — the previous
	// parser turned this into nil and stored "no cap"), and something even
	// worse in monthly (a hand-crafted value that overflowed int64). Both
	// must reject the whole submission.
	form := url.Values{}
	form.Set("daily_budget", "0")
	form.Set("monthly_budget", "1e16")
	form.Set("yearly_budget", "")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("orgSlug", org.Slug())
	r.SetPathValue("appID", string(app.ID()))
	// renderApplicationQuotaFormError re-renders the page, which calls
	// BaseURLString: a minimal valid URL keeps the renderer happy.
	base, _ := url.Parse("http://example.test")
	r = r.WithContext(httpCtx.SetBaseURL(r.Context(), base.String()))

	rec := httptest.NewRecorder()
	h.saveApplicationQuota(rec, r)

	// 1. No success redirect: the bug also surfaces as a misleading banner.
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "success=saved") {
		t.Errorf("Location = %q, must not redirect to ?success=saved on a rejected submit", loc)
	}
	// 2. The store was not overwritten: the pointer the stub held before
	// the request is the same one it holds after.
	if qStore.quota != existing {
		t.Errorf("store quota pointer changed; SetQuota was called on a rejected submit")
	}
	if qStore.quota.DailyBudget() == nil || *qStore.quota.DailyBudget() != daily {
		var got int64
		if qStore.quota.DailyBudget() != nil {
			got = *qStore.quota.DailyBudget()
		}
		t.Errorf("existing daily budget was overwritten: got %d, want %d", got, daily)
	}
	// 3. The form is re-rendered with the bad values, so the operator sees
	// their input and the reason it was rejected (HTML5 validation is
	// bypassed so a hand-crafted POST gets the same response).
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	lower := strings.ToLower(body)
	for _, want := range []string{"plafond invalide", "mensuel", "valeur invalide"} {
		if !strings.Contains(lower, want) {
			t.Errorf("body missing %q", want)
		}
	}
	// The operator's typed 0 should still be in the daily input: re-rendering
	// with an empty field would suggest the form swallowed the value, the
	// exact failure mode the ticket describes.
	if !strings.Contains(body, `value="0"`) {
		t.Errorf("re-rendered form does not echo the operator's typed 0; got body:\n%s", body)
	}
}

// TestSaveOrgQuotaZeroSucceeds covers the positive case for the new "0 means
// strict zero cap" semantics: an operator typing 0 to freeze spending must
// still get a successful save (issue #88 lists this as the most likely
// scenario). Without this test the parser change could regress the freeze
// use case.
func TestSaveOrgQuotaZeroSucceeds(t *testing.T) {
	org := model.NewOrganization("tenant", "acme", "Acme", "EUR")
	qStore := &stubQuotaStore{}

	h := &Handler{
		orgStore:   &stubOrgStore{org: org},
		quotaStore: qStore,
		usageStore: &stubUsageStore{},
	}

	form := url.Values{}
	form.Set("daily_budget", "0")
	form.Set("monthly_budget", "")
	form.Set("yearly_budget", "")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("orgSlug", org.Slug())

	rec := httptest.NewRecorder()
	h.saveOrgQuota(rec, r)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "success=saved") {
		t.Errorf("Location = %q, want success=saved redirect", loc)
	}
	if qStore.quota == nil {
		t.Fatalf("SetQuota was not called")
	}
	if qStore.quota.DailyBudget() == nil || *qStore.quota.DailyBudget() != 0 {
		var got int64
		if qStore.quota.DailyBudget() != nil {
			got = *qStore.quota.DailyBudget()
		}
		t.Errorf("daily budget = %v, want pointer to 0 (strict zero cap)", got)
	}
	if qStore.quota.MonthlyBudget() != nil || qStore.quota.YearlyBudget() != nil {
		t.Errorf("empty fields should be stored as nil (unlimited), got %v / %v",
			qStore.quota.MonthlyBudget(), qStore.quota.YearlyBudget())
	}
}

// membershipWithUser is the test-only constructor for the quota member
// tests: it bundles a user into a membership the way gorm's preload
// would at runtime, since model.NewMembership has no public setter for
// the preloaded user. Implements model.Membership.
type membershipWithUser struct {
	id     model.MembershipID
	userID model.UserID
	orgID  model.OrgID
	user   model.User
}

func (m membershipWithUser) ID() model.MembershipID  { return m.id }
func (m membershipWithUser) UserID() model.UserID    { return m.userID }
func (m membershipWithUser) OrgID() model.OrgID      { return m.orgID }
func (m membershipWithUser) CreatedAt() time.Time    { return time.Time{} }
func (m membershipWithUser) User() model.User        { return m.user }
func (m membershipWithUser) Org() model.Organization { return nil }
func (m membershipWithUser) Roles() []model.Role     { return nil }

// TestSaveOrgQuotaBadValueDoesNotOverwriteBudget is the org-scoped mirror of
// TestSaveApplicationQuotaBadValueDoesNotOverwriteBudget. The re-render
// path is shared, but the per-scope wiring (loader, scope ID, breadcrumbs)
// diverged when the three helpers were extracted, so each scope is pinned
// independently. A regression in the org path must not pass on the
// application test alone.
func TestSaveOrgQuotaBadValueDoesNotOverwriteBudget(t *testing.T) {
	org := model.NewOrganization("tenant", "acme", "Acme", "EUR")
	daily := int64(8_000_000) // 8.00
	existing := model.NewQuota(model.QuotaScopeOrg, string(org.ID()), "EUR", &daily, nil, nil)
	qStore := &stubQuotaStore{quota: existing, quotaScope: model.QuotaScopeOrg}

	h := &Handler{
		orgStore:   &stubOrgStore{org: org},
		quotaStore: qStore,
		usageStore: &stubUsageStore{},
	}

	// 1e16 is the ticket's overflow example: the previous parser silently
	// dropped it to nil (unlimited). With the fix it must reject.
	form := url.Values{}
	form.Set("daily_budget", "1e16")
	form.Set("monthly_budget", "")
	form.Set("yearly_budget", "")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("orgSlug", org.Slug())
	base, _ := url.Parse("http://example.test")
	r = r.WithContext(httpCtx.SetBaseURL(r.Context(), base.String()))

	rec := httptest.NewRecorder()
	h.saveOrgQuota(rec, r)

	if loc := rec.Header().Get("Location"); strings.Contains(loc, "success=saved") {
		t.Errorf("Location = %q, must not redirect to ?success=saved on a rejected submit", loc)
	}
	if qStore.quota != existing {
		t.Errorf("store quota pointer changed; SetQuota was called on a rejected submit")
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
	body := strings.ToLower(rec.Body.String())
	for _, want := range []string{"plafond invalide", "journalier", "valeur invalide"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	if !strings.Contains(rec.Body.String(), `value="1e16"`) {
		t.Errorf("re-rendered form does not echo the operator's typed 1e16")
	}
}

// TestSaveMemberQuotaBadValueDoesNotOverwriteBudget is the per-member mirror
// of the application regression test. It exercises the per-member loader
// and the member-specific breadcrumbs, which diverge from the org helper.
func TestSaveMemberQuotaBadValueDoesNotOverwriteBudget(t *testing.T) {
	org := model.NewOrganization("tenant", "acme", "Acme", "EUR")
	otherUser := model.NewUser(model.TenantID("tenant"), "alice", "alice-sub", "alice@example.com", "Alice", true)
	membership := membershipWithUser{
		id:     model.NewMembershipID(),
		userID: otherUser.ID(),
		orgID:  org.ID(),
		user:   otherUser,
	}

	daily := int64(3_000_000) // 3.00
	existing := model.NewQuota(model.QuotaScopeUser, string(membership.UserID()), "EUR", &daily, nil, nil)
	qStore := &stubQuotaStore{quota: existing, quotaScope: model.QuotaScopeUser}

	h := &Handler{
		orgStore:   &stubOrgStore{org: org, membership: membership},
		quotaStore: qStore,
		usageStore: &stubUsageStore{},
	}

	// Negative value: hand-crafted POST, since the form's min="0" rejects
	// it client-side. The previous parser dropped it to nil silently.
	form := url.Values{}
	form.Set("daily_budget", "-5")
	form.Set("monthly_budget", "")
	form.Set("yearly_budget", "")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("orgSlug", org.Slug())
	r.SetPathValue("membershipID", string(membership.ID()))
	base, _ := url.Parse("http://example.test")
	r = r.WithContext(httpCtx.SetBaseURL(r.Context(), base.String()))

	rec := httptest.NewRecorder()
	h.saveMemberQuota(rec, r)

	if loc := rec.Header().Get("Location"); strings.Contains(loc, "success=saved") {
		t.Errorf("Location = %q, must not redirect to ?success=saved on a rejected submit", loc)
	}
	if qStore.quota != existing {
		t.Errorf("store quota pointer changed; SetQuota was called on a rejected submit")
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
	body := strings.ToLower(rec.Body.String())
	for _, want := range []string{"plafond invalide", "journalier"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	if !strings.Contains(rec.Body.String(), `value="-5"`) {
		t.Errorf("re-rendered form does not echo the operator's typed -5")
	}
}

// TestSaveMemberQuotaForeignMembership covers the cross-org privilege
// escalation the Conclave review flagged on PR #111: an operator with
// PermQuotaWrite on their own org must not write a QuotaScopeUser row
// for a membership that belongs to another org, since QuotaStore keys
// user quotas by userID alone (no org column) and one user can belong to
// several orgs. membershipFromQuotaPath is the fix; this test pins the
// answer is 404 and the store is not touched.
func TestSaveMemberQuotaForeignMembership(t *testing.T) {
	// Operator's own org.
	attackerOrg := model.NewOrganization("tenant-attacker", "attacker", "Attacker", "EUR")
	// Membership in a *different* org — the operator has no business
	// editing this user's quota from the attacker org's admin.
	victimOrg := model.NewOrganization("tenant-victim", "victim", "Victim", "EUR")
	victimUser := model.NewUser(model.TenantID("tenant-victim"), "victim-user", "victim-sub", "victim@example.com", "Victim User", true)
	foreignMembership := model.NewMembership(victimUser.ID(), victimOrg.ID())

	qStore := &stubQuotaStore{}

	h := &Handler{
		orgStore:   &stubOrgStore{org: attackerOrg, membership: foreignMembership},
		quotaStore: qStore,
		usageStore: &stubUsageStore{},
	}

	form := url.Values{}
	form.Set("daily_budget", "10")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("orgSlug", attackerOrg.Slug())
	r.SetPathValue("membershipID", string(foreignMembership.ID()))

	rec := httptest.NewRecorder()
	h.saveMemberQuota(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (foreign membership must look like a not-found)", rec.Code)
	}
	if qStore.quota != nil {
		t.Errorf("SetQuota was called for a foreign membership")
	}
	// No redirect either: the operator must not even see a misleading
	// banner or a preserved previous value through the redirect.
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("Location = %q, want empty (no redirect on cross-org 404)", loc)
	}
}

// TestGetMemberQuotaPageForeignMembership mirrors the save handler test for
// the GET path: the same flaw affected getMemberQuotaPage (it used the
// same membership lookup) and the fix covers both routes.
func TestGetMemberQuotaPageForeignMembership(t *testing.T) {
	attackerOrg := model.NewOrganization("tenant-attacker", "attacker", "Attacker", "EUR")
	victimOrg := model.NewOrganization("tenant-victim", "victim", "Victim", "EUR")
	victimUser := model.NewUser(model.TenantID("tenant-victim"), "victim-user", "victim-sub", "victim@example.com", "Victim User", true)
	foreignMembership := model.NewMembership(victimUser.ID(), victimOrg.ID())

	qStore := &stubQuotaStore{}

	h := &Handler{
		orgStore:   &stubOrgStore{org: attackerOrg, membership: foreignMembership},
		quotaStore: qStore,
		usageStore: &stubUsageStore{},
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.SetPathValue("orgSlug", attackerOrg.Slug())
	r.SetPathValue("membershipID", string(foreignMembership.ID()))

	rec := httptest.NewRecorder()
	h.getMemberQuotaPage(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (foreign membership must look like a not-found)", rec.Code)
	}
}

// TestSaveApplicationQuotaReRenderPreservesUnpostedFields is the regression
// test for the silent-data-loss bug Conclave review #2 caught on PR #111:
// when an operator submits only the field they want to edit (the others
// are absent from the form), the re-render must show the stored values
// for the unposted fields, and a follow-up save must preserve them.
//
// Before the fix, quotaFormSubmitted used r.FormValue which returns ""
// for both absent and empty values, and budgetInputValue treated the
// empty string as authoritative. The re-render blanked every unposted
// field; the next submit stored nil (unlimited) for each, silently
// wiping the budget the operator never touched.
func TestSaveApplicationQuotaReRenderPreservesUnpostedFields(t *testing.T) {
	org := model.NewOrganization("tenant", "acme", "Acme", "EUR")
	app := model.NewApplication(org.ID(), "acme-app", "", true)

	daily := int64(5_000_000)    // 5.00
	monthly := int64(40_000_000) // 40.00
	yearly := int64(200_000_000) // 200.00
	existing := model.NewQuota(model.QuotaScopeApplication, string(app.ID()), "EUR", &daily, &monthly, &yearly)
	qStore := &stubQuotaStore{quota: existing, quotaScope: model.QuotaScopeApplication}

	h := handlerWithStubs(org, app, qStore, &stubUsageStore{})

	// Operator only typed into daily (bad value), left monthly and yearly
	// out of the form entirely. The form's min="0" + browser validation
	// would normally block this, but a hand-crafted POST reproduces the
	// exact regression path.
	form := url.Values{}
	form.Set("daily_budget", "abc")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("orgSlug", org.Slug())
	r.SetPathValue("appID", string(app.ID()))
	base, _ := url.Parse("http://example.test")
	r = r.WithContext(httpCtx.SetBaseURL(r.Context(), base.String()))

	rec := httptest.NewRecorder()
	h.saveApplicationQuota(rec, r)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	body := rec.Body.String()

	// (a) The bad value is echoed in the daily field.
	if !strings.Contains(body, `value="abc"`) {
		t.Errorf("re-rendered form does not echo the operator's typed abc in daily; got body:\n%s", body)
	}
	// (b) The stored monthly and yearly values are re-rendered, not blank.
	// formatBudgetField returns the value in whole units: 40.00 and 200.00.
	for _, want := range []string{`value="40"`, `value="200"`} {
		if !strings.Contains(body, want) {
			t.Errorf("re-rendered form is missing %q; the stored value was wiped by the re-render", want)
		}
	}
	// (c) The store was not touched.
	if qStore.quota != existing {
		t.Errorf("store quota pointer changed; SetQuota was called on a rejected submit")
	}

	// Now simulate the operator fixing daily and resubmitting. With the
	// fix, monthly=40 and yearly=200 survive because the re-render did
	// not blank them and parseQuotaBudgetFields sees them via
	// budgetFieldValue (no Submitted entry, falls back to stored).
	fixForm := url.Values{}
	fixForm.Set("daily_budget", "7")
	fixForm.Set("monthly_budget", "40")
	fixForm.Set("yearly_budget", "200")
	r2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fixForm.Encode()))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r2.SetPathValue("orgSlug", org.Slug())
	r2.SetPathValue("appID", string(app.ID()))

	rec2 := httptest.NewRecorder()
	h.saveApplicationQuota(rec2, r2)

	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("follow-up save: status = %d, want 303", rec2.Code)
	}
	if qStore.quota.DailyBudget() == nil || *qStore.quota.DailyBudget() != 7_000_000 {
		var got int64
		if qStore.quota.DailyBudget() != nil {
			got = *qStore.quota.DailyBudget()
		}
		t.Errorf("follow-up save daily budget = %d, want 7_000_000", got)
	}
	if qStore.quota.MonthlyBudget() == nil || *qStore.quota.MonthlyBudget() != monthly {
		var got int64
		if qStore.quota.MonthlyBudget() != nil {
			got = *qStore.quota.MonthlyBudget()
		}
		t.Errorf("follow-up save wiped monthly: got %d, want %d (stored value must survive)", got, monthly)
	}
	if qStore.quota.YearlyBudget() == nil || *qStore.quota.YearlyBudget() != yearly {
		var got int64
		if qStore.quota.YearlyBudget() != nil {
			got = *qStore.quota.YearlyBudget()
		}
		t.Errorf("follow-up save wiped yearly: got %d, want %d (stored value must survive)", got, yearly)
	}
}
