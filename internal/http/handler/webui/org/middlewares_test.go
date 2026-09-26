package org

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// stubMiddlewareOrgStore resolves a single organization by slug. The handlers
// under test only need orgFromSlug to succeed: they look up the middleware by
// ID and then check OrgID() against the resolved org.
type stubMiddlewareOrgStore struct {
	port.OrgStore
	org model.Organization
}

func (s *stubMiddlewareOrgStore) GetOrgBySlug(_ context.Context, _ model.TenantID, slug string) (model.Organization, error) {
	if s.org != nil && s.org.Slug() == slug {
		return s.org, nil
	}
	return nil, port.ErrNotFound
}

// stubMiddlewareStore exposes a single middleware to GetMiddlewareByID. The
// SaveMiddleware / DeleteMiddleware stubs are here so a regression that moved
// the ownership check past the mutation cannot pass silently — they record the
// call and t.Fatal if it happens.
type stubMiddlewareStore struct {
	port.MiddlewareStore
	mw          model.Middleware
	saved       []model.Middleware
	deleteCalls []model.MiddlewareID
}

func (s *stubMiddlewareStore) GetMiddlewareByID(_ context.Context, id model.MiddlewareID) (model.Middleware, error) {
	if s.mw != nil && s.mw.ID() == id {
		return s.mw, nil
	}
	return nil, port.ErrNotFound
}

func (s *stubMiddlewareStore) SaveMiddleware(_ context.Context, m model.Middleware) error {
	s.saved = append(s.saved, m)
	return nil
}

func (s *stubMiddlewareStore) DeleteMiddleware(_ context.Context, id model.MiddlewareID) error {
	s.deleteCalls = append(s.deleteCalls, id)
	return nil
}

// TestMiddlewareHandlers_RejectCrossOrgAccess is a regression test for the
// IDOR closed by #70: the five org-middleware handlers load the middleware by
// ID only, so without an explicit OrgID() check a user from org A could read,
// mutate, enable/disable, delete, or open the pipeline editor of a middleware
// that belongs to org B.
//
// On a cross-org request every handler must answer 404 *before* the mutation
// runs: a 200 with the foreign payload, or a 200 followed by a Save / Delete
// against the victim's middleware, would re-open the leak.
func TestMiddlewareHandlers_RejectCrossOrgAccess(t *testing.T) {
	const testTenantID model.TenantID = "test-tenant"

	attackerOrg := model.NewOrganization(testTenantID, "org-a", "Org A", "")
	victimOrg := model.NewOrganization(testTenantID, "org-b", "Org B", "")

	// A middleware owned by org B. The attacker holds no permission over org B.
	victimMW := model.NewMiddleware(victimOrg.ID(), "victim", "owned by B")

	orgStore := &stubMiddlewareOrgStore{org: attackerOrg}
	mwStore := &stubMiddlewareStore{mw: victimMW}
	h := &Handler{orgStore: orgStore, middlewareStore: mwStore}

	cases := []struct {
		name   string
		method string
		path   string
		invoke func(http.ResponseWriter, *http.Request)
	}{
		{
			name:   "edit page",
			method: http.MethodGet,
			path:   "/orgs/org-a/admin/middlewares/" + string(victimMW.ID()) + "/edit",
			invoke: h.getEditMiddlewarePage,
		},
		{
			name:   "update",
			method: http.MethodPost,
			path:   "/orgs/org-a/admin/middlewares/" + string(victimMW.ID()) + "/edit",
			invoke: h.updateMiddleware,
		},
		{
			name:   "toggle",
			method: http.MethodPost,
			path:   "/orgs/org-a/admin/middlewares/" + string(victimMW.ID()) + "/toggle",
			invoke: h.toggleMiddleware,
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			path:   "/orgs/org-a/admin/middlewares/" + string(victimMW.ID()),
			invoke: h.deleteMiddleware,
		},
		{
			name:   "pipeline editor",
			method: http.MethodGet,
			path:   "/orgs/org-a/admin/middlewares/" + string(victimMW.ID()) + "/pipeline",
			invoke: h.getMiddlewarePipelineEditorPage,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.SetPathValue("orgSlug", attackerOrg.Slug())
			req.SetPathValue("middlewareID", string(victimMW.ID()))

			rec := httptest.NewRecorder()
			tc.invoke(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d (body=%q)", rec.Code, rec.Body.String())
			}

			if len(mwStore.saved) != 0 {
				t.Fatalf("SaveMiddleware called on a foreign middleware: %v", mwStore.saved)
			}
			if len(mwStore.deleteCalls) != 0 {
				t.Fatalf("DeleteMiddleware called on a foreign middleware: %v", mwStore.deleteCalls)
			}
		})
	}
}
