package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
)

type cleanupStore struct {
	port.OrgStore
	port.VirtualModelStore
	port.MiddlewareStore
	port.PersonalVirtualModelStore
	port.SecretStore
	org                model.Organization
	vm                 model.VirtualModel
	mw                 model.Middleware
	pvm                model.PersonalVirtualModel
	lookupErr, saveErr error
	effects            []string
	pruned             [][3]string
}

func (s *cleanupStore) GetOrgBySlug(_ context.Context, tenant model.TenantID, slug string) (model.Organization, error) {
	if s.org.TenantID() != tenant || s.org.Slug() != slug {
		return nil, port.ErrNotFound
	}
	return s.org, nil
}
func (s *cleanupStore) GetVirtualModelByID(context.Context, model.VirtualModelID) (model.VirtualModel, error) {
	return s.vm, s.lookupErr
}
func (s *cleanupStore) GetMiddlewareByID(context.Context, model.MiddlewareID) (model.Middleware, error) {
	return s.mw, s.lookupErr
}
func (s *cleanupStore) GetPersonalVirtualModelByID(context.Context, model.PersonalVirtualModelID) (model.PersonalVirtualModel, error) {
	return s.pvm, s.lookupErr
}
func (s *cleanupStore) SaveVirtualModel(context.Context, model.VirtualModel) error {
	s.effects = append(s.effects, "save")
	return s.saveErr
}
func (s *cleanupStore) SaveMiddleware(context.Context, model.Middleware) error {
	s.effects = append(s.effects, "save")
	return s.saveErr
}
func (s *cleanupStore) SavePersonalVirtualModel(context.Context, model.PersonalVirtualModel) error {
	s.effects = append(s.effects, "save")
	return s.saveErr
}
func (s *cleanupStore) DeleteVirtualModel(context.Context, model.VirtualModelID) error {
	s.effects = append(s.effects, "delete")
	return s.saveErr
}
func (s *cleanupStore) DeleteMiddleware(context.Context, model.MiddlewareID) error {
	s.effects = append(s.effects, "delete")
	return s.saveErr
}
func (s *cleanupStore) DeletePersonalVirtualModel(context.Context, model.PersonalVirtualModelID) error {
	s.effects = append(s.effects, "delete")
	return s.saveErr
}
func (s *cleanupStore) DeleteAllForNode(_ context.Context, scope, plugin, node string) error {
	s.effects = append(s.effects, "prune")
	s.pruned = append(s.pruned, [3]string{scope, plugin, node})
	return nil
}

func TestPipelineHTTPSecretCleanup(t *testing.T) {
	for _, kind := range []string{"virtual", "middleware", "personal"} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			for _, scenario := range []string{"success", "same-plugin", "builtin", "malformed", "save-failure", "foreign-resource", "foreign-tenant", "missing", "technical"} {
				for _, role := range []string{"user", "admin"} {
					t.Run(strings.Join([]string{kind, method, scenario, role}, "/"), func(t *testing.T) {
						tenant := model.NewTenant("tenant", "Tenant", "")
						org := model.NewOrganization(tenant.ID(), "org", "Org", "")
						user := model.NewUser(tenant.ID(), "test", "user", "u@test", "User", true, role)
						ownerOrg, ownerUser := org.ID(), user.ID()
						if scenario == "foreign-resource" {
							ownerOrg, ownerUser = "foreign-org", "foreign-user"
						}
						graph := &model.PipelineGraph{Nodes: []model.PipelineNode{{ID: "node", Type: model.NodeTypePlugin, Data: []byte(`{"pluginName":"old"}`)}}}
						vm := model.NewVirtualModel(ownerOrg, "vm", "")
						vm.SetGraph(graph)
						mw := model.NewMiddleware(ownerOrg, "mw", "")
						mw.SetGraph(graph)
						pvm := model.NewPersonalVirtualModel(ownerUser, "pvm", "")
						pvm.SetGraph(graph)
						store := &cleanupStore{org: org, vm: vm, mw: mw, pvm: pvm}
						if scenario == "save-failure" {
							store.saveErr = errors.New("save failed")
						}
						if scenario == "missing" {
							store.lookupErr = port.ErrNotFound
						}
						if scenario == "technical" {
							store.lookupErr = errors.New("database failed")
						}
						body := `{"graph":{"nodes":[{"id":"node","type":"plugin","data":{"pluginName":"new"}}]}}`
						if scenario == "same-plugin" {
							body = strings.ReplaceAll(body, `"new"`, `"old"`)
						}
						if scenario == "builtin" {
							body = `{"graph":{"nodes":[{"id":"node","type":"model"}]}}`
						}
						if scenario == "malformed" {
							body = `{"graph":{"nodes":[{"id":"node","type":"plugin"}]}}`
						}
						path, scope := "/api/orgs/org/virtual-models/"+string(vm.ID()), string(org.ID())
						if kind == "middleware" {
							path = "/api/orgs/org/middlewares/" + string(mw.ID())
						}
						if kind == "personal" {
							path = "/api/personal-models/" + string(pvm.ID())
							scope = "~:" + string(user.ID())
						}
						req := httptest.NewRequest(method, path, strings.NewReader(body))
						if scenario == "foreign-tenant" {
							tenant = model.NewTenant("foreign", "Foreign", "")
						}
						ctx := httpCtx.SetTenant(httpCtx.SetUser(req.Context(), user), tenant)
						ctx = httpCtx.SetPermissionResolver(ctx, func(context.Context, model.OrgID) (rbac.PermissionSet, error) {
							return rbac.NewPermissionSet([]string{string(rbac.PermVirtualModelsWrite), string(rbac.PermMiddlewaresWrite)}, nil), nil
						})
						rec := httptest.NewRecorder()
						h := NewHandler(nil, store, store, store, store, store, nil, nil)
						h.ServeHTTP(rec, req.WithContext(ctx))
						switch scenario {
						case "foreign-resource", "foreign-tenant", "missing":
							require.Equal(t, http.StatusNotFound, rec.Code)
							require.Empty(t, store.effects)
						case "technical":
							require.Equal(t, http.StatusInternalServerError, rec.Code)
							require.Empty(t, store.effects)
						case "save-failure":
							require.Equal(t, http.StatusInternalServerError, rec.Code)
							require.Empty(t, store.pruned)
						case "malformed":
							// A plugin node naming no plugin is refused before
							// anything is saved or pruned.
							if method == http.MethodPut {
								require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
								require.Empty(t, store.effects)
								break
							}
							require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
							require.Equal(t, [][3]string{{scope, "old", "node"}}, store.pruned)
						default:
							want := http.StatusOK
							if method == http.MethodDelete {
								want = http.StatusNoContent
							}
							require.Equal(t, want, rec.Code, rec.Body.String())
							if method == http.MethodPut && scenario == "same-plugin" {
								require.Empty(t, store.pruned)
							} else {
								require.Equal(t, [][3]string{{scope, "old", "node"}}, store.pruned)
								require.Equal(t, "prune", store.effects[1], "cleanup follows successful persistence")
							}
						}
					})
				}
			}
		}
	}
}
