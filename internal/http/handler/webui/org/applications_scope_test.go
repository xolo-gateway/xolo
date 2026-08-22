package org

import (
	"context"
	"errors"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// stubOrgStore resolves a single organization by slug and fails every other
// call. Only GetOrgBySlug is exercised by resolveOrgAndApplication.
type stubOrgStore struct {
	port.OrgStore
	org model.Organization
}

func (s *stubOrgStore) GetOrgBySlug(_ context.Context, _ model.TenantID, slug string) (model.Organization, error) {
	if s.org != nil && s.org.Slug() == slug {
		return s.org, nil
	}
	return nil, port.ErrNotFound
}

type stubApplicationStore struct {
	port.ApplicationStore
	app model.Application
}

func (s *stubApplicationStore) GetApplication(_ context.Context, appID model.ApplicationID) (model.Application, error) {
	if s.app != nil && s.app.ID() == appID {
		return s.app, nil
	}
	return nil, port.ErrNotFound
}

// TestResolveOrgAndApplication_RejectsForeignApplication covers the cross-org
// leak: an operator holding permissions on their own org must not reach an
// application owned by another org, since GetApplication is keyed by
// application id alone.
func TestResolveOrgAndApplication_RejectsForeignApplication(t *testing.T) {
	victimOrg := model.NewOrganization("tenant-victim", "victim", "Victim", "")
	foreignApp := model.NewApplication(victimOrg.ID(), "victim-app", "", true)

	attackerOrg := model.NewOrganization("tenant-attacker", "attacker", "Attacker", "")

	h := &Handler{
		orgStore:         &stubOrgStore{org: attackerOrg},
		applicationStore: &stubApplicationStore{app: foreignApp},
	}

	_, _, err := h.resolveOrgAndApplication(context.Background(), attackerOrg.Slug(), string(foreignApp.ID()))
	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("expected port.ErrNotFound for an application of another org, got %v", err)
	}
}

func TestResolveOrgAndApplication_AcceptsOwnApplication(t *testing.T) {
	org := model.NewOrganization("tenant", "acme", "Acme", "")
	app := model.NewApplication(org.ID(), "acme-app", "", true)

	h := &Handler{
		orgStore:         &stubOrgStore{org: org},
		applicationStore: &stubApplicationStore{app: app},
	}

	gotOrg, gotApp, err := h.resolveOrgAndApplication(context.Background(), org.Slug(), string(app.ID()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOrg.ID() != org.ID() {
		t.Errorf("org: got %v, want %v", gotOrg.ID(), org.ID())
	}
	if gotApp.ID() != app.ID() {
		t.Errorf("app: got %v, want %v", gotApp.ID(), app.ID())
	}
}

func TestResolveOrgAndApplication_UnknownApplication(t *testing.T) {
	org := model.NewOrganization("tenant", "acme", "Acme", "")

	h := &Handler{
		orgStore:         &stubOrgStore{org: org},
		applicationStore: &stubApplicationStore{},
	}

	if _, _, err := h.resolveOrgAndApplication(context.Background(), org.Slug(), "unknown"); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("expected port.ErrNotFound, got %v", err)
	}
}
