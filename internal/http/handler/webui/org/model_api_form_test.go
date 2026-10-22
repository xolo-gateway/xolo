package org

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

// modelFormStore serves one organization and its provider, and records the
// models created. Unimplemented methods panic.
type modelFormStore struct {
	port.OrgStore
	port.ProviderStore
	org      model.Organization
	provider model.Provider
	created  []model.LLMModel
	saved    []model.LLMModel
	// createErr, when set, fails every CreateLLMModel.
	createErr error
}

func (s *modelFormStore) GetOrgBySlug(_ context.Context, _ model.TenantID, slug string) (model.Organization, error) {
	if slug != s.org.Slug() {
		return nil, port.ErrNotFound
	}
	return s.org, nil
}

func (s *modelFormStore) GetProviderByID(_ context.Context, id model.ProviderID) (model.Provider, error) {
	if id != s.provider.ID() {
		return nil, port.ErrNotFound
	}
	return s.provider, nil
}

func (s *modelFormStore) GetLLMModelByProxyName(_ context.Context, _ model.OrgID, name string) (model.LLMModel, error) {
	for _, m := range s.created {
		if m.ProxyName() == name {
			return m, nil
		}
	}
	return nil, port.ErrNotFound
}

func (s *modelFormStore) ListLLMModels(context.Context, model.OrgID) ([]model.LLMModel, error) {
	return s.created, nil
}

func (s *modelFormStore) CreateLLMModel(_ context.Context, m model.LLMModel) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.created = append(s.created, m)
	return nil
}

func postCreateModel(t *testing.T, fields url.Values, configure ...func(*Handler)) (*httptest.ResponseRecorder, *modelFormStore) {
	t.Helper()
	tenant := model.NewTenant("a", "A", "")
	org := model.NewOrganization(tenant.ID(), "acme", "Acme", "")
	s := &modelFormStore{
		org:      org,
		provider: model.NewProvider(org.ID(), "OpenCode Go", "openai", "https://opencode.ai/inference/go/openai/v1", "ciphertext", "USD"),
	}
	h := &Handler{orgStore: s, providerStore: s}
	for _, c := range configure {
		c(h)
	}

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := httpCtx.SetBaseURL(httpCtx.SetTenant(req.Context(), tenant), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, model.NewUser(tenant.ID(), "test", "admin", "admin@example.test", "Admin", true, model.PlatformRoleAdmin))
	req = req.WithContext(ctx)
	req.SetPathValue("orgSlug", org.Slug())
	req.SetPathValue("providerID", string(s.provider.ID()))

	rr := httptest.NewRecorder()
	h.createModel(rr, req)
	return rr, s
}

func TestCreateModel_StoresTheAPIOverride(t *testing.T) {
	rr, s := postCreateModel(t, url.Values{
		"proxy_name":   {"qwen"},
		"real_model":   {"qwen3.8-max"},
		"api_type":     {"anthropic"},
		"api_base_url": {"https://opencode.ai/inference/go/anthropic/v1"},
	})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect: %s", rr.Code, rr.Body.String())
	}
	if len(s.created) != 1 {
		t.Fatalf("created %d models, want 1", len(s.created))
	}
	if m := s.created[0]; m.APIType() != "anthropic" || m.BaseURL() != "https://opencode.ai/inference/go/anthropic/v1" {
		t.Errorf("stored type=%q url=%q", m.APIType(), m.BaseURL())
	}
}

func TestCreateModel_InvalidAPIOverrideRendersTheForm(t *testing.T) {
	for name, fields := range map[string]url.Values{
		"unknown type":     {"api_type": {"google"}},
		"relative URL":     {"api_base_url": {"example.com/v1"}},
		"URL with secrets": {"api_base_url": {"https://user:secret@example.com/v1"}},
		"URL with a query": {"api_base_url": {"https://example.com/v1?token=abc"}},
		"URL with a hash":  {"api_base_url": {"https://example.com/v1#frag"}},
	} {
		t.Run(name, func(t *testing.T) {
			fields.Set("proxy_name", "qwen")
			fields.Set("real_model", "qwen3.8-max")
			rr, s := postCreateModel(t, fields)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rr.Code)
			}
			if !strings.Contains(rr.Body.String(), "Accès à l") {
				t.Errorf("the form does not show the API error")
			}
			if len(s.created) != 0 {
				t.Errorf("created %d models, want none", len(s.created))
			}
		})
	}
}

func (s *modelFormStore) GetLLMModelByID(_ context.Context, id model.LLMModelID) (model.LLMModel, error) {
	for _, m := range s.created {
		if m.ID() == id {
			return m, nil
		}
	}
	return nil, port.ErrNotFound
}

func (s *modelFormStore) SaveLLMModel(_ context.Context, m model.LLMModel) error {
	s.saved = append(s.saved, m)
	return nil
}

func postUpdateModel(t *testing.T, existing *model.BaseLLMModel, fields url.Values, configure ...func(*Handler)) (*httptest.ResponseRecorder, *modelFormStore) {
	t.Helper()
	tenant := model.NewTenant("a", "A", "")
	org := model.NewOrganization(tenant.ID(), "acme", "Acme", "")
	provider := model.NewProvider(org.ID(), "OpenCode Go", "openai", "https://opencode.ai/inference/go/openai/v1", "ciphertext", "USD")
	m := model.NewLLMModel(provider.ID(), org.ID(), existing.ProxyName(), existing.RealModel(), "", 0, 0)
	m.SetAPIType(existing.APIType())
	m.SetBaseURL(existing.BaseURL())
	m.SetKind(existing.Kind())
	s := &modelFormStore{org: org, provider: provider, created: []model.LLMModel{m}}
	h := &Handler{orgStore: s, providerStore: s}
	for _, c := range configure {
		c(h)
	}

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := httpCtx.SetBaseURL(httpCtx.SetTenant(req.Context(), tenant), "http://xolo.test")
	ctx = httpCtx.SetUser(ctx, model.NewUser(tenant.ID(), "test", "admin", "admin@example.test", "Admin", true, model.PlatformRoleAdmin))
	req = req.WithContext(ctx)
	req.SetPathValue("orgSlug", org.Slug())
	req.SetPathValue("providerID", string(provider.ID()))
	req.SetPathValue("modelID", string(m.ID()))

	rr := httptest.NewRecorder()
	h.updateModel(rr, req)
	return rr, s
}

func TestUpdateModel_SetsAndClearsTheAPIOverride(t *testing.T) {
	plain := model.NewLLMModel("", "", "qwen", "qwen3.8-max", "", 0, 0)
	rr, s := postUpdateModel(t, plain, url.Values{
		"proxy_name":   {"qwen"},
		"real_model":   {"qwen3.8-max"},
		"api_type":     {"anthropic"},
		"api_base_url": {"https://opencode.ai/inference/go/anthropic/v1"},
	})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect: %s", rr.Code, rr.Body.String())
	}
	if len(s.saved) != 1 || s.saved[0].APIType() != "anthropic" || s.saved[0].BaseURL() != "https://opencode.ai/inference/go/anthropic/v1" {
		t.Fatalf("saved %+v, want the override", s.saved)
	}

	overridden := model.NewLLMModel("", "", "qwen", "qwen3.8-max", "", 0, 0)
	overridden.SetAPIType("anthropic")
	overridden.SetBaseURL("https://opencode.ai/inference/go/anthropic/v1")
	rr, s = postUpdateModel(t, overridden, url.Values{"proxy_name": {"qwen"}, "real_model": {"qwen3.8-max"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect", rr.Code)
	}
	if len(s.saved) != 1 || s.saved[0].APIType() != "" || s.saved[0].BaseURL() != "" {
		t.Errorf("empty fields must clear the override back to the provider's")
	}
}

func TestUpdateModel_InvalidAPIOverrideRendersTheModel(t *testing.T) {
	existing := model.NewLLMModel("", "", "qwen", "qwen3.8-max", "", 0, 0)
	rr, s := postUpdateModel(t, existing, url.Values{
		"proxy_name":   {"qwen"},
		"real_model":   {"qwen3.8-max"},
		"api_base_url": {"ftp://example.com"},
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Accès à l") || !strings.Contains(rr.Body.String(), "qwen") {
		t.Errorf("the form must show the error and the existing model")
	}
	if len(s.saved) != 0 {
		t.Errorf("saved %d models, want none", len(s.saved))
	}
}
