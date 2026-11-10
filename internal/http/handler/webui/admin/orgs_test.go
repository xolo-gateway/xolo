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
	created   []model.Organization
	createErr error
}

func (s *orgFormStore) CreateOrg(_ context.Context, org model.Organization) error {
	if s.createErr != nil {
		return errors.WithStack(s.createErr)
	}
	s.created = append(s.created, org)
	return nil
}

func postCreateOrg(t *testing.T, fields url.Values, configure ...func(*orgFormStore)) (*httptest.ResponseRecorder, *orgFormStore) {
	t.Helper()
	tenant := model.NewTenant("a", "A", "")
	store := &orgFormStore{}
	for _, c := range configure {
		c(store)
	}
	h := &Handler{orgStore: store, roleStore: stubRoleStore{}}

	req := httptest.NewRequest(http.MethodPost, "/admin/orgs", strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := httpCtx.SetBaseURL(httpCtx.SetTenant(req.Context(), tenant), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, model.NewUser(tenant.ID(), "test", "admin", "admin@example.test", "Admin", true, model.PlatformRoleAdmin))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.createOrg(rec, req)
	return rec, store
}

// stubRoleStore only needs EnsureBuiltinRoles.
type stubRoleStore struct {
	port.RoleStore
}

func (stubRoleStore) EnsureBuiltinRoles(context.Context, model.OrgID) error { return nil }

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
