package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// orgFormStore records the organisations createOrg writes.
type orgFormStore struct {
	port.OrgStore
	org       model.Organization
	created   []model.Organization
	saved     []model.Organization
	createErr error
	saveErr   error
	getErr    error
}

func (s *orgFormStore) CreateOrg(_ context.Context, org model.Organization) error {
	if s.createErr != nil {
		return errors.WithStack(s.createErr)
	}
	s.created = append(s.created, org)
	return nil
}

func (s *orgFormStore) GetOrgByID(_ context.Context, _ model.OrgID) (model.Organization, error) {
	if s.getErr != nil {
		return nil, errors.WithStack(s.getErr)
	}
	if s.org == nil {
		return nil, errors.Wrap(port.ErrNotFound, "no such organization")
	}
	return s.org, nil
}

func (s *orgFormStore) SaveOrg(_ context.Context, org model.Organization) error {
	if s.saveErr != nil {
		return errors.WithStack(s.saveErr)
	}
	s.saved = append(s.saved, org)
	return nil
}

// newOrgRequest carries the tenant and a platform admin, as the middleware would.
func newOrgRequest(t *testing.T, target string, body string) *http.Request {
	t.Helper()
	tenant := model.NewTenant("a", "A", "")
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := httpCtx.SetBaseURL(httpCtx.SetTenant(req.Context(), tenant), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, model.NewUser(tenant.ID(), "test", "admin", "admin@example.test", "Admin", true, model.PlatformRoleAdmin))
	return req.WithContext(ctx)
}

func postCreateOrg(t *testing.T, fields url.Values, configure ...func(*orgFormStore)) (*httptest.ResponseRecorder, *orgFormStore) {
	t.Helper()
	store := &orgFormStore{}
	for _, c := range configure {
		c(store)
	}
	h := &Handler{orgStore: store, roleStore: stubRoleStore{}}

	rec := httptest.NewRecorder()
	h.createOrg(rec, newOrgRequest(t, "/admin/orgs", fields.Encode()))
	return rec, store
}

// postCreateOrgWithRoles is postCreateOrg with the role store under control.
func postCreateOrgWithRoles(t *testing.T, fields url.Values, roleStore port.RoleStore) (*httptest.ResponseRecorder, *orgFormStore) {
	t.Helper()
	store := &orgFormStore{}
	h := &Handler{orgStore: store, roleStore: roleStore}

	rec := httptest.NewRecorder()
	h.createOrg(rec, newOrgRequest(t, "/admin/orgs", fields.Encode()))
	return rec, store
}

// postUpdateOrg posts the edition form for a stored organisation.
func postUpdateOrg(t *testing.T, fields url.Values, configure ...func(*orgFormStore)) (*httptest.ResponseRecorder, *orgFormStore) {
	t.Helper()
	store := &orgFormStore{org: model.NewOrganization(model.NewTenant("a", "A", "").ID(), "acme", "Acme Corp", "The first one")}
	for _, c := range configure {
		c(store)
	}
	h := &Handler{orgStore: store, roleStore: stubRoleStore{}}

	req := newOrgRequest(t, "/admin/orgs/acme/edit", fields.Encode())
	req.SetPathValue("orgID", string(store.org.ID()))
	rec := httptest.NewRecorder()
	h.updateOrg(rec, req)
	return rec, store
}

// stubRoleStore only needs EnsureBuiltinRoles.
type stubRoleStore struct {
	port.RoleStore
	err error
}

func (s stubRoleStore) EnsureBuiltinRoles(context.Context, model.OrgID) error { return s.err }

// TestCreateOrg_MissingSlugRendersTheForm is the regression test for the blank
// page.
func TestCreateOrg_MissingSlugRendersTheForm(t *testing.T) {
	rec, store := postCreateOrg(t, url.Values{
		"name":        {"Acme Corp"},
		"description": {"The first one"},
	})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="content"`) {
		t.Error("the response carries no #content: a boosted submit would swap the page for nothing")
	}
	if !strings.Contains(body, "L&#39;identifiant de l&#39;organisation est requis.") {
		t.Error("the response does not state that the identifier is required")
	}
	if !strings.Contains(body, `value="Acme Corp"`) || !strings.Contains(body, `value="The first one"`) {
		t.Error("the submitted values are not refilled into the form")
	}
	if len(store.created) != 0 {
		t.Fatalf("created %d organizations on a refused submit, want none", len(store.created))
	}
}

func TestCreateOrg_MissingNameRendersTheForm(t *testing.T) {
	rec, store := postCreateOrg(t, url.Values{"slug": {"acme"}})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Le nom de l&#39;organisation est requis.") {
		t.Error("the response does not state that the name is required")
	}
	if len(store.created) != 0 {
		t.Fatal("created an organization on a refused submit")
	}
}

func TestCreateOrg_InvalidSlugRendersTheForm(t *testing.T) {
	for name, slug := range map[string]string{
		"uppercase":     "Acme",
		"inner space":   "acme corp",
		"trailing dash": "acme-",
		"too long":      strings.Repeat("a", model.MaxSlugLength+1),
	} {
		t.Run(name, func(t *testing.T) {
			rec, store := postCreateOrg(t, url.Values{"name": {"Acme"}, "slug": {slug}})

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "L&#39;identifiant ne peut contenir que des minuscules") {
				t.Error("the response does not explain the expected identifier shape")
			}
			if len(store.created) != 0 {
				t.Fatal("created an organization with an invalid slug")
			}
		})
	}
}

func TestCreateOrg_SlugTakenRendersTheForm(t *testing.T) {
	rec, _ := postCreateOrg(t,
		url.Values{"name": {"Acme"}, "slug": {"acme"}},
		func(s *orgFormStore) { s.createErr = errors.Wrap(port.ErrAlreadyExists, "slug is taken") },
	)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "utilise déjà l&#39;identifiant « acme »") {
		t.Error("the response does not name the identifier that is taken")
	}
}

func TestCreateOrg_StoresTheTrimmedFields(t *testing.T) {
	rec, store := postCreateOrg(t, url.Values{
		"name":        {"  Acme Corp  "},
		"slug":        {" acme "},
		"description": {"  The first one "},
	})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect: %s", rec.Code, rec.Body.String())
	}
	if len(store.created) != 1 {
		t.Fatalf("created %d organizations, want 1", len(store.created))
	}
	org := store.created[0]
	if got, want := org.Name(), "Acme Corp"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := org.Slug(), "acme"; got != want {
		t.Errorf("slug = %q, want %q", got, want)
	}
	if got, want := org.Description(), "The first one"; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}
}

// TestCreateOrg_StoreFailureRendersTheErrorPage : la branche d'échec du store
// répondait un texte brut, que la coquille hx-boostée remplaçait par rien.
func TestCreateOrg_StoreFailureRendersTheErrorPage(t *testing.T) {
	rec, _ := postCreateOrg(t, url.Values{"name": {"Acme"}, "slug": {"acme"}},
		func(s *orgFormStore) { s.createErr = errors.New("store is down") })

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `id="content"`) {
		t.Error("the response carries no #content: a boosted submit would swap the page for nothing")
	}
}

// TestCreateOrg_RolesNotProvisionedSaysTheOrganizationExists : l'organisation
// est déjà enregistrée à ce stade, et sans message distinct l'opérateur resoumet
// et tombe sur « identifiant déjà utilisé ».
func TestCreateOrg_RolesNotProvisionedSaysTheOrganizationExists(t *testing.T) {
	rec, store := postCreateOrgWithRoles(t,
		url.Values{"name": {"Acme"}, "slug": {"acme"}},
		stubRoleStore{err: errors.New("role store is down")},
	)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="content"`) {
		t.Error("the response carries no #content")
	}
	if !strings.Contains(body, "mais ses rôles par défaut n") {
		t.Error("the response does not state that the roles were not provisioned")
	}
	if len(store.created) != 1 {
		t.Fatalf("created %d organizations, want 1: the organization is stored before its roles", len(store.created))
	}
}

func TestCreateOrg_UnparsableBodyRendersTheForm(t *testing.T) {
	h := &Handler{orgStore: &orgFormStore{}, roleStore: stubRoleStore{}}

	rec := httptest.NewRecorder()
	h.createOrg(rec, newOrgRequest(t, "/admin/orgs", "name=%zz"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="content"`) {
		t.Error("the response carries no #content")
	}
	if !strings.Contains(body, "incomplet ou mal formé") {
		t.Error("the response does not explain that the submitted form could not be read")
	}
}

func TestUpdateOrg_SaveFailureKeepsTheSubmittedValues(t *testing.T) {
	rec, store := postUpdateOrg(t,
		url.Values{"name": {"Acme Renamed"}, "description": {"Renamed"}, "active": {"on"}},
		func(s *orgFormStore) { s.saveErr = errors.New("store is down") },
	)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="content"`) {
		t.Error("the response carries no #content")
	}
	if !strings.Contains(body, "pas pu être enregistrée") {
		t.Error("the response does not state that the organization was not saved")
	}
	if !strings.Contains(body, `value="Acme Renamed"`) {
		t.Error("the submitted name is not refilled into the form")
	}
	if len(store.saved) != 0 {
		t.Fatalf("saved %d organizations on a failed save, want none", len(store.saved))
	}
}

func TestUpdateOrg_UnknownOrgRendersTheErrorPage(t *testing.T) {
	rec, _ := postUpdateOrg(t, url.Values{"name": {"Acme"}},
		func(s *orgFormStore) { s.getErr = port.ErrNotFound })

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="content"`) {
		t.Error("the response carries no #content")
	}
	if !strings.Contains(body, "introuvable") {
		t.Error("the response does not state that the organization was not found")
	}
}

func TestUpdateOrg_SavesTheSubmittedFields(t *testing.T) {
	rec, store := postUpdateOrg(t, url.Values{
		"name":        {"Acme Renamed"},
		"description": {"Renamed"},
		"active":      {"on"},
	})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect: %s", rec.Code, rec.Body.String())
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved %d organizations, want 1", len(store.saved))
	}
	if got, want := store.saved[0].Name(), "Acme Renamed"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if !store.saved[0].Active() {
		t.Error("the organization was not activated")
	}
}
