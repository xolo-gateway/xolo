package component

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
)

// testMembership lets the template under test see m.Org() != nil. The real
// `model.NewMembership` constructor only wires the IDs: the GORM adapter
// pre-loads `Org` and exposes it through a wrapper. The templ then guards
// the select-item rendering on `if m.Org() != nil`, so to drive that branch
// in a unit test we need a Membership that returns a non-nil Org.
type testMembership struct {
	id        model.MembershipID
	userID    model.UserID
	orgID     model.OrgID
	createdAt time.Time
	org       model.Organization
}

func (m *testMembership) ID() model.MembershipID  { return m.id }
func (m *testMembership) UserID() model.UserID    { return m.userID }
func (m *testMembership) OrgID() model.OrgID      { return m.orgID }
func (m *testMembership) CreatedAt() time.Time    { return m.createdAt }
func (m *testMembership) User() model.User        { return nil }
func (m *testMembership) Org() model.Organization { return m.org }
func (m *testMembership) Roles() []model.Role     { return nil }

func newMembershipWithOrg(user model.User, org model.Organization) *testMembership {
	return &testMembership{
		id:        model.NewMembershipID(),
		userID:    user.ID(),
		orgID:     org.ID(),
		createdAt: time.Now(),
		org:       org,
	}
}

// TestTokensPage_ShowsOrgRequiredErrorAndReopensDialog pins the second half
// of the fix for issue #109. When the previous create-token request bounced
// back with `error=org_required`, the page must:
//
//   - render the friendly error message inside the dialog (not just in a
//     side banner that the user might not see);
//   - paint the org selectbox trigger in the destructive palette so the
//     missing field is visually obvious;
//   - open the dialog automatically so the user does not have to hunt for
//     the "New key" button;
//   - preserve whatever they typed so the redirect did not throw their
//     work away.
//
// A regression in any of those four properties re-opens the original bug.
func TestTokensPage_ShowsOrgRequiredErrorAndReopensDialog(t *testing.T) {
	const tenantID model.TenantID = "test-tenant"

	user := model.NewUser(tenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)
	org := model.NewOrganization(tenantID, "acme", "Acme", "Acme corp")

	ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, user)
	ctx = httpCtx.SetMemberships(ctx, nil)

	vmodel := TokensPageVModel{
		OrgMemberships: []model.Membership{newMembershipWithOrg(user, org)},
		Error:          "org_required",
		OpenDialog:     true,
		FormLabel:      "Mon poste",
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "tokens",
			Context:      common.ContextPersonal,
		},
	}

	var out strings.Builder
	if err := TokensPage(vmodel).Render(ctx, &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()

	// 1. The friendly error must be visible inside the dialog body.
	if !strings.Contains(html, "Veuillez sélectionner une organisation") {
		t.Errorf("expected the friendly org-required message to appear in the HTML")
	}

	// 2. The selectbox trigger must carry the destructive palette AND the
	//    aria-invalid marker, so the missing field is both visually and
	//    semantically flagged. The TriggerProps.HasError knob the
	//    templui component exposes applies both (selectbox.templ Trigger
	//    class merge + aria-invalid), and is what the page uses here.
	if !strings.Contains(html, "border-destructive") {
		t.Errorf("expected the selectbox trigger to be flagged in the destructive palette")
	}
	if !strings.Contains(html, `aria-invalid="true"`) {
		t.Errorf("expected aria-invalid=true on the selectbox trigger so assistive tech flags the field")
	}

	// 3. The dialog must render in its open state on first load: otherwise
	//    the error message above is hidden behind a closed dialog and the
	//    user has no idea why the form bounced.
	if !strings.Contains(html, `data-tui-dialog-open="true"`) {
		t.Errorf("expected the dialog to be open on first render when Error is set")
	}

	// 4. The user's draft must be preserved across the redirect.
	if !strings.Contains(html, `value="Mon poste"`) {
		t.Errorf("expected the label to be pre-filled across the redirect")
	}
}

// TestTokensPage_ShowsLabelRequiredError covers the symmetric `label_required`
// branch. The dialog should also reopen and the label input must be flagged
// as invalid.
func TestTokensPage_ShowsLabelRequiredError(t *testing.T) {
	const tenantID model.TenantID = "test-tenant"

	user := model.NewUser(tenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)
	org := model.NewOrganization(tenantID, "acme", "Acme", "Acme corp")

	ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, user)
	ctx = httpCtx.SetMemberships(ctx, nil)

	vmodel := TokensPageVModel{
		OrgMemberships: []model.Membership{newMembershipWithOrg(user, org)},
		Error:          "label_required",
		OpenDialog:     true,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "tokens",
			Context:      common.ContextPersonal,
		},
	}

	var out strings.Builder
	if err := TokensPage(vmodel).Render(ctx, &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()

	if !strings.Contains(html, "Le nom de la clé est obligatoire") {
		t.Errorf("expected the label-required message to appear in the HTML")
	}
	if !strings.Contains(html, `aria-invalid="true"`) {
		t.Errorf("expected the label input to carry aria-invalid=true")
	}
	if !strings.Contains(html, `data-tui-dialog-open="true"`) {
		t.Errorf("expected the dialog to be open on first render when Error is set")
	}
}

// TestTokensPage_DialogStaysClosedWhenNoError guards against the reverse
// regression: the dialog must NOT open on a plain visit to the page, only
// after the previous request failed. Opening unconditionally would pop the
// dialog in the user's face every time they land on the screen.
func TestTokensPage_DialogStaysClosedWhenNoError(t *testing.T) {
	const tenantID model.TenantID = "test-tenant"

	user := model.NewUser(tenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)
	org := model.NewOrganization(tenantID, "acme", "Acme", "Acme corp")

	ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, user)
	ctx = httpCtx.SetMemberships(ctx, nil)

	vmodel := TokensPageVModel{
		OrgMemberships: []model.Membership{newMembershipWithOrg(user, org)},
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "tokens",
			Context:      common.ContextPersonal,
		},
	}

	var out strings.Builder
	if err := TokensPage(vmodel).Render(ctx, &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()

	if strings.Contains(html, `data-tui-dialog-open="true"`) {
		t.Errorf("dialog should stay closed when there is no error to surface")
	}
}

// TestTokensPage_PreSelectsOrgWhenFormOrgIDSet locks in the round-trip: when
// the previous request bounced back, the matching organisation should already
// be selected in the dialog so the user does not have to pick it again.
func TestTokensPage_PreSelectsOrgWhenFormOrgIDSet(t *testing.T) {
	const tenantID model.TenantID = "test-tenant"

	user := model.NewUser(tenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)
	org := model.NewOrganization(tenantID, "acme", "Acme", "Acme corp")

	ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, user)
	ctx = httpCtx.SetMemberships(ctx, nil)

	vmodel := TokensPageVModel{
		OrgMemberships: []model.Membership{newMembershipWithOrg(user, org)},
		FormOrgID:      org.ID(),
		OpenDialog:     true,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "tokens",
			Context:      common.ContextPersonal,
		},
	}

	var out strings.Builder
	if err := TokensPage(vmodel).Render(ctx, &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()

	// Find the select-item for our org and check that it carries the
	// selected marker the templui component reads on init.
	marker := `data-tui-selectbox-value="` + string(org.ID()) + `"`
	idx := strings.Index(html, marker)
	if idx == -1 {
		t.Fatalf("expected the org item to be rendered with value=%q", org.ID())
	}

	// The "selected" attribute sits on the same div as data-tui-selectbox-value.
	// Look at the next ~250 chars (the rest of the div open tag).
	end := idx + 250
	if end > len(html) {
		end = len(html)
	}
	window := html[idx:end]
	if !strings.Contains(window, `data-tui-selectbox-selected="true"`) {
		t.Errorf("expected the pre-selected org item to carry data-tui-selectbox-selected=true, got: %s", window)
	}
}

// TestTokensPage_CreatedTokenKeepsDialogClosed is the post-success invariant
// flagged by the Conclave review of #119. When a key has just been created,
// the clear-text token is rendered in a page-level alert (Clé API créée avec
// succès + InlineSnippet). Opening the modal on top of that alert would dim
// it under the overlay and force the user to dismiss an empty form before
// they could copy the one-time key. The dialog must therefore stay closed
// whenever vmodel.CreatedToken is non-empty, regardless of the other state.
func TestTokensPage_CreatedTokenKeepsDialogClosed(t *testing.T) {
	const tenantID model.TenantID = "test-tenant"

	user := model.NewUser(tenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)
	org := model.NewOrganization(tenantID, "acme", "Acme", "Acme corp")

	ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, user)
	ctx = httpCtx.SetMemberships(ctx, nil)

	const clearText = "xolo-demo-1-2b-3c-4d-5e-6f-7g-8h-9i-10j-12k"
	vmodel := TokensPageVModel{
		OrgMemberships: []model.Membership{newMembershipWithOrg(user, org)},
		AuthTokens:     []model.AuthToken{},
		CreatedToken:   clearText,
		// OpenDialog intentionally left false, the way the handler now sets
		// it on a successful create. If a future change re-introduces an
		// unconditional reopen on success, this test must catch it.
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "tokens",
			Context:      common.ContextPersonal,
		},
	}

	var out strings.Builder
	if err := TokensPage(vmodel).Render(ctx, &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()

	if !strings.Contains(html, "Clé API créée avec succès") {
		t.Errorf("expected the post-success alert to be present so the user sees the clear-text key")
	}
	if !strings.Contains(html, clearText) {
		t.Errorf("expected the clear-text token to be embedded in the alert")
	}

	// The dialog must not auto-open: doing so would put a 50%-black overlay
	// over the alert, and the modal has no path to the clear-text token. We
	// check the rendered HTML rather than vmodel.OpenDialog because the
	// handler is what populates that field, and the bug it surfaced was that
	// the handler was setting OpenDialog=true whenever CreatedToken was set.
	if strings.Contains(html, `data-tui-dialog-open="true"`) {
		t.Errorf("dialog must stay closed after a successful create: opening it would dim the clear-text alert")
	}
}

// TestTokensPage_OrgRequiredWithNoMembershipsHidesAlert locks in the
// contradiction fix: when the user belongs to no organisation, the dialog
// already tells them to contact an admin. Adding "Veuillez sélectionner
// une organisation" on top reads as two opposing pieces of guidance, and
// the suppression helper is the only thing standing between the two. A
// regression there would re-introduce the contradiction in a single line.
func TestTokensPage_OrgRequiredWithNoMembershipsHidesAlert(t *testing.T) {
	const tenantID model.TenantID = "test-tenant"

	user := model.NewUser(tenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)

	ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, user)
	ctx = httpCtx.SetMemberships(ctx, nil)

	vmodel := TokensPageVModel{
		OrgMemberships: nil, // fresh account, no memberships yet
		Error:          "org_required",
		OpenDialog:     true,
		AppLayoutVModel: common.AppLayoutVModel{
			User:         user,
			SelectedItem: "tokens",
			Context:      common.ContextPersonal,
		},
	}

	var out strings.Builder
	if err := TokensPage(vmodel).Render(ctx, &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()

	// The form description must still guide the user toward an admin.
	if !strings.Contains(html, "Vous n’appartenez à aucune organisation") {
		t.Errorf("expected the empty-memberships hint to remain in the form description")
	}
	// The contradictory alert must be suppressed in this combination.
	if strings.Contains(html, "Veuillez sélectionner une organisation") {
		t.Errorf("org_required alert must not stack with the empty-memberships description: the user has nothing to pick")
	}
	// Other codes (label_required, etc.) must NOT be affected by the
	// suppression: their alerts still surface even when memberships is empty.
	labelCtx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	labelCtx = httpCtx.SetUser(labelCtx, user)
	labelVmodel := vmodel
	labelVmodel.Error = "label_required"
	var labelOut strings.Builder
	if err := TokensPage(labelVmodel).Render(labelCtx, &labelOut); err != nil {
		t.Fatalf("render (label): %v", err)
	}
	if !strings.Contains(labelOut.String(), "Le nom de la clé est obligatoire.") {
		t.Errorf("label_required alert must still surface even with no memberships")
	}
}
