package profile

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// stubUserStore records the auth tokens it is asked to create and serves an
// empty list. The fields that get exercised by the handlers under test are
// the only ones implemented; the rest panic to catch any unexpected reach.
type stubUserStore struct {
	port.UserStore
	created []model.AuthToken
}

func (s *stubUserStore) GetUserAuthTokens(_ context.Context, _ model.UserID) ([]model.AuthToken, error) {
	return nil, nil
}

func (s *stubUserStore) CreateAuthToken(_ context.Context, token model.AuthToken) error {
	s.created = append(s.created, token)
	return nil
}

// stubOrgStore serves a single membership so the form has an organisation to
// bind to. Membership details are read by getTokensPage for the table that
// lists existing tokens, so the stub returns the same membership it was built
// for and an empty list otherwise.
//
// memberOrgIDs records the orgs the user is a member of (the ones in
// memberships), which IsMember reads. Tests that exercise a cross-org POST
// post an org_id that is NOT in this set.
type stubOrgStore struct {
	port.OrgStore
	memberships  []model.Membership
	memberOrgIDs map[model.OrgID]struct{}
	isErr        error
}

func (s *stubOrgStore) GetUserMemberships(_ context.Context, _ model.UserID) ([]model.Membership, error) {
	return s.memberships, nil
}

func (s *stubOrgStore) IsMember(_ context.Context, _ model.UserID, orgID model.OrgID) (bool, error) {
	if s.isErr != nil {
		return false, s.isErr
	}
	_, ok := s.memberOrgIDs[orgID]
	return ok, nil
}

// newTestHandler wires the minimum surface area used by createToken:
// userStore (for CreateAuthToken) and orgStore (for GetUserMemberships).
// Anything else is nil on purpose — the handlers never reach it on the
// redirect paths exercised here, and a panic would surface a regression.
func newTestHandler(t *testing.T, userStore port.UserStore, orgStore port.OrgStore) *Handler {
	t.Helper()
	h := NewHandler(userStore, orgStore, nil, nil, nil, nil)
	return h
}

// authRequest injects the authenticated user into the request context. The
// real middleware stack does this from the OIDC session; in unit tests we
// skip the middleware and put the user straight into context. The base URL
// is set as well, since the tokens template builds the form action through
// common.BaseURL, which panics if the request context carries none.
func authRequest(r *http.Request, user model.User) *http.Request {
	ctx := httpCtx.SetUser(r.Context(), user)
	ctx = httpCtx.SetBaseURL(ctx, "http://xolo.test")
	return r.WithContext(ctx)
}

// TestCreateToken_MissingOrgIDRedirects pins the fix for issue #109:
//
// Before the fix the handler returned a plain-text 400 page; the browser
// navigated away from the form and the user saw a white page with no clue
// what had gone wrong. The expected behaviour is now a 303 redirect back to
// the tokens page with `?error=org_required`, preserving whatever the user
// had already typed so they do not have to retype it from scratch.
func TestCreateToken_MissingOrgIDRedirects(t *testing.T) {
	const testTenantID model.TenantID = "test-tenant"

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)

	userStore := &stubUserStore{}
	orgStore := &stubOrgStore{memberships: []model.Membership{model.NewMembership(user.ID(), org.ID())}, memberOrgIDs: map[model.OrgID]struct{}{org.ID(): {}}}
	h := newTestHandler(t, userStore, orgStore)

	form := url.Values{}
	form.Set("label", "Mon poste de travail")
	form.Set("expires_at", "2030-12-31")
	// org_id intentionally left blank.

	req := httptest.NewRequest(http.MethodPost, "/profile/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = authRequest(req, user)

	rr := httptest.NewRecorder()
	h.createToken(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d: %s", rr.Code, rr.Body.String())
	}

	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/profile/tokens?") {
		t.Fatalf("expected redirect to /profile/tokens, got %q", loc)
	}

	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("redirect URL not parseable: %v", err)
	}

	q := parsed.Query()
	if got := q.Get("error"); got != "org_required" {
		t.Errorf("expected error=org_required, got %q", got)
	}
	if got := q.Get("label"); got != "Mon poste de travail" {
		t.Errorf("expected label to be preserved, got %q", got)
	}
	if got := q.Get("expires_at"); got != "2030-12-31" {
		t.Errorf("expected expires_at to be preserved, got %q", got)
	}
	// `error` is the only signal that drives the dialog state on render; the
	// redirect URL must stay minimal and not invent query parameters that the
	// handler never reads (it has no way to know which side of the line the
	// request bounced from otherwise).
	if got := q.Get("open"); got != "" {
		t.Errorf("redirect URL must not carry an `open` parameter the handler ignores, got %q", got)
	}

	if len(userStore.created) != 0 {
		t.Errorf("no token should be created on validation failure, got %d", len(userStore.created))
	}
}

// TestCreateToken_MissingLabelRedirects is the symmetrical check for the
// `label_required` branch. Same redirect shape, different error code; the
// handler should never answer 400 with plain text for either field.
func TestCreateToken_MissingLabelRedirects(t *testing.T) {
	const testTenantID model.TenantID = "test-tenant"

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)

	userStore := &stubUserStore{}
	orgStore := &stubOrgStore{memberships: []model.Membership{model.NewMembership(user.ID(), org.ID())}, memberOrgIDs: map[model.OrgID]struct{}{org.ID(): {}}}
	h := newTestHandler(t, userStore, orgStore)

	form := url.Values{}
	form.Set("label", "   ")
	form.Set("org_id", string(org.ID()))

	req := httptest.NewRequest(http.MethodPost, "/profile/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = authRequest(req, user)

	rr := httptest.NewRecorder()
	h.createToken(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	q, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("redirect URL not parseable: %v", err)
	}
	if got := q.Query().Get("error"); got != "label_required" {
		t.Errorf("expected error=label_required, got %q", got)
	}

	if len(userStore.created) != 0 {
		t.Errorf("no token should be created on validation failure, got %d", len(userStore.created))
	}
}

// TestCreateToken_DoesNotReturnPlainText400 is a backstop. Issue #109 was
// diagnosed from the symptom "white page" — the handler used to call
// http.Error(w, ..., http.StatusBadRequest), which sends a 400 with a
// plain-text body that the browser renders as the entire page. Any handler
// that touches createToken must NOT do that, regardless of the validation
// reason: the response body must be empty (redirect) or HTML (template),
// never text/plain.
func TestCreateToken_DoesNotReturnPlainText400(t *testing.T) {
	const testTenantID model.TenantID = "test-tenant"

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)

	orgStore := &stubOrgStore{memberships: []model.Membership{model.NewMembership(user.ID(), org.ID())}, memberOrgIDs: map[model.OrgID]struct{}{org.ID(): {}}}
	userStore := &stubUserStore{}
	h := newTestHandler(t, userStore, orgStore)

	cases := []struct {
		name  string
		form  url.Values
		check func(t *testing.T, rr *httptest.ResponseRecorder)
	}{
		{
			name: "missing org_id",
			form: url.Values{"label": {"x"}},
		},
		{
			name: "missing label",
			form: url.Values{"org_id": {string(org.ID())}},
		},
		{
			name: "blank label",
			form: url.Values{"label": {"   "}, "org_id": {string(org.ID())}},
		},
		{
			name: "blank org_id",
			form: url.Values{"label": {"x"}, "org_id": {"   "}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/profile/tokens", strings.NewReader(tc.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req = authRequest(req, user)

			rr := httptest.NewRecorder()
			h.createToken(rr, req)

			ct := rr.Header().Get("Content-Type")
			if strings.HasPrefix(ct, "text/plain") && rr.Code >= 400 {
				t.Fatalf("handler returned plain-text %d, would render as a white page: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestRedirectTokensWithFormURL escapes values that may contain spaces,
// ampersands, or other reserved characters. A label like "Production &
// staging" must reach the next request intact, otherwise the redirect is
// silently dropping the user's input.
func TestRedirectTokensWithFormURL(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/profile/tokens", strings.NewReader("label=Production+%26+staging&org_id=acme&expires_at=2030-01-01"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}

	got := redirectTokensWithForm(r, "org_required")

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("redirect URL not parseable: %v", err)
	}
	q := parsed.Query()

	if got := q.Get("error"); got != "org_required" {
		t.Errorf("error code lost: %q", got)
	}
	if got := q.Get("label"); got != "Production & staging" {
		t.Errorf("label not round-tripped: %q", got)
	}
	if got := q.Get("org_id"); got != "acme" {
		t.Errorf("org_id not round-tripped: %q", got)
	}
	if got := q.Get("expires_at"); got != "2030-01-01" {
		t.Errorf("expires_at not round-tripped: %q", got)
	}
}

// TestCreateToken_SuccessRendersClearTextAndKeepsDialogClosed locks in the
// post-success behaviour. Two things must hold at once, and they are easy to
// break together: the clear-text token must reach the response body (it is the
// only time it is ever shown), AND the dialog must stay closed — opening it
// would dim the page-level alert that holds the token behind the modal
// overlay, forcing the user to dismiss an empty form before they can copy
// the one-time key. Conclave review of #119 caught a regression where
// openDialog was set whenever createdToken != ""; this test is the
// backstop so the regression cannot return silently.
func TestCreateToken_SuccessRendersClearTextAndKeepsDialogClosed(t *testing.T) {
	const testTenantID model.TenantID = "test-tenant"

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)

	userStore := &stubUserStore{}
	orgStore := &stubOrgStore{memberships: []model.Membership{model.NewMembership(user.ID(), org.ID())}, memberOrgIDs: map[model.OrgID]struct{}{org.ID(): {}}}
	h := newTestHandler(t, userStore, orgStore)

	form := url.Values{}
	form.Set("label", "Mon poste")
	form.Set("org_id", string(org.ID()))

	req := httptest.NewRequest(http.MethodPost, "/profile/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = authRequest(req, user)

	rr := httptest.NewRecorder()
	h.createToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (page rendered with the new key), got %d: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, "Clé API créée avec succès") {
		t.Errorf("the post-success alert is missing from the response: the clear-text token would be unreachable")
	}
	if !strings.Contains(body, "Copiez cette clé maintenant") {
		t.Errorf("the alert hint is missing: the user must know this is the only time the key is shown")
	}

	// Exactly one token should have been persisted.
	if len(userStore.created) != 1 {
		t.Fatalf("expected exactly one token to be persisted, got %d", len(userStore.created))
	}
	created := userStore.created[0]
	if created.Label() != "Mon poste" {
		t.Errorf("persisted label = %q, want %q", created.Label(), "Mon poste")
	}
	if string(created.OrgID()) != string(org.ID()) {
		t.Errorf("persisted org_id = %q, want %q", created.OrgID(), org.ID())
	}

	// No redirect on success.
	if loc := rr.Header().Get("Location"); loc != "" {
		t.Errorf("success must render in place, not redirect; got Location=%q", loc)
	}

	// The dialog must NOT open on success: a token has been returned inside
	// the page-level alert, and reopening the modal would dim it under the
	// overlay. Conclave review of #119 caught a regression where the dialog
	// reopened whenever CreatedToken was set; the templ-rendered HTML is in
	// body and must not carry the data-tui-dialog-open="true" marker.
	if strings.Contains(body, `data-tui-dialog-open="true"`) {
		t.Errorf("dialog must stay closed after a successful create: opening it would dim the clear-text alert")
	}
}

// TestCreateToken_ForeignOrgIDIsRejected closes the authorization gap a
// reviewer caught on #119: createToken used to accept any non-empty value
// as the AuthToken's OrgID, even one belonging to an org the caller was
// not a member of. The AuthToken's OrgID is later trusted by auth_extractor
// to resolve models and quotas, so a foreign org_id let any caller mint a
// key against an organisation they should not be able to touch. The fix
// calls OrgStore.IsMember before CreateAuthToken and redirects with
// error=org_not_member otherwise. No token must ever be persisted on the
// rejected path.
func TestCreateToken_ForeignOrgIDIsRejected(t *testing.T) {
	const testTenantID model.TenantID = "test-tenant"

	// The caller is a member of acme...
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)
	mine := model.NewOrganization(testTenantID, "acme", "Acme", "")
	// ...but the POST claims a key for globex, where they hold no membership.
	theirs := model.NewOrganization(testTenantID, "globex", "Globex", "")

	userStore := &stubUserStore{}
	orgStore := &stubOrgStore{
		memberships:  []model.Membership{model.NewMembership(user.ID(), mine.ID())},
		memberOrgIDs: map[model.OrgID]struct{}{mine.ID(): {}},
	}
	h := newTestHandler(t, userStore, orgStore)

	form := url.Values{}
	form.Set("label", "Tentative")
	form.Set("org_id", string(theirs.ID()))

	req := httptest.NewRequest(http.MethodPost, "/profile/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = authRequest(req, user)

	rr := httptest.NewRecorder()
	h.createToken(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	q, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("redirect URL not parseable: %v", err)
	}
	if got := q.Query().Get("error"); got != "org_not_member" {
		t.Errorf("error = %q, want org_not_member", got)
	}
	// The draft is preserved so the user can fix the value and resubmit.
	if got := q.Query().Get("label"); got != "Tentative" {
		t.Errorf("label lost in the redirect, got %q", got)
	}

	if len(userStore.created) != 0 {
		t.Fatalf("no token must be persisted on a foreign org_id, got %d", len(userStore.created))
	}
}

// TestCreateToken_OrgLookupErrorRedirectsSafely covers the branch where
// IsMember returns an infrastructure error (DB blip, etc.). The user must
// not be left looking at a 500 page; the handler redirects back to the
// form with a dedicated error code so they can retry, and no token is
// persisted.
func TestCreateToken_OrgLookupErrorRedirectsSafely(t *testing.T) {
	const testTenantID model.TenantID = "test-tenant"

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)

	userStore := &stubUserStore{}
	orgStore := &stubOrgStore{
		memberships:  []model.Membership{model.NewMembership(user.ID(), org.ID())},
		memberOrgIDs: map[model.OrgID]struct{}{org.ID(): {}},
		isErr:        errors.New("database connection pool: timeout"),
	}
	h := newTestHandler(t, userStore, orgStore)

	form := url.Values{}
	form.Set("label", "Poste")
	form.Set("org_id", string(org.ID()))

	req := httptest.NewRequest(http.MethodPost, "/profile/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = authRequest(req, user)

	rr := httptest.NewRecorder()
	h.createToken(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rr.Code)
	}
	q, _ := url.Parse(rr.Header().Get("Location"))
	if got := q.Query().Get("error"); got != "org_lookup_failed" {
		t.Errorf("error = %q, want org_lookup_failed", got)
	}

	if len(userStore.created) != 0 {
		t.Errorf("no token must be persisted on a lookup error, got %d", len(userStore.created))
	}
}

// TestCreateToken_InvalidExpiresAtRedirects is the backstop for a path
// that used to swallow the parse error and create a key with no expiry.
// The user typed a value that did not match the format; the handler must
// surface that, not silently downcast it to "no expiry".
func TestCreateToken_InvalidExpiresAtRedirects(t *testing.T) {
	const testTenantID model.TenantID = "test-tenant"

	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	user := model.NewUser(testTenantID, "test", "subject", "user@xolo.test", "Ada", true, model.PlatformRoleUser)

	userStore := &stubUserStore{}
	orgStore := &stubOrgStore{memberships: []model.Membership{model.NewMembership(user.ID(), org.ID())}, memberOrgIDs: map[model.OrgID]struct{}{org.ID(): {}}}
	h := newTestHandler(t, userStore, orgStore)

	form := url.Values{}
	form.Set("label", "Poste")
	form.Set("org_id", string(org.ID()))
	form.Set("expires_at", "pas-une-date")

	req := httptest.NewRequest(http.MethodPost, "/profile/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = authRequest(req, user)

	rr := httptest.NewRecorder()
	h.createToken(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rr.Code)
	}
	q, _ := url.Parse(rr.Header().Get("Location"))
	if got := q.Query().Get("error"); got != "invalid_expires_at" {
		t.Errorf("error = %q, want invalid_expires_at", got)
	}
	if got := q.Query().Get("expires_at"); got != "pas-une-date" {
		t.Errorf("expires_at not round-tripped, got %q", got)
	}

	if len(userStore.created) != 0 {
		t.Errorf("no token must be persisted on a malformed date, got %d", len(userStore.created))
	}
}
