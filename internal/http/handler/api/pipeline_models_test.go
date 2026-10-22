package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/api"
)

// TestPersonalPipelineModelsFollowTheChargedTeam pins the picker of the
// personal pipeline editor on what the proxy accepts: a personal virtual
// model charging a team only reaches the models of that team's organization,
// and nothing while its owner is no longer in the team (see
// proxy.TeamScopeHook).
func TestPersonalPipelineModelsFollowTheChargedTeam(t *testing.T) {
	orgA := model.NewOrganization(testTenantID, "acme", "ACME", "")
	orgB := model.NewOrganization(testTenantID, "umbrella", "Umbrella", "")
	granted := model.NewLLMModel(model.NewProviderID(), orgA.ID(), "gpt-4o", "openai/gpt-4o", "", 100, 200)
	ungranted := model.NewLLMModel(model.NewProviderID(), orgA.ID(), "mistral", "mistral/large", "", 100, 200)
	other := model.NewLLMModel(model.NewProviderID(), orgB.ID(), "claude", "anthropic/claude", "", 100, 200)
	// A pipeline holds a conversation: the models of another kind are never
	// offered, however usable.
	jev := model.NewLLMModel(model.NewProviderID(), orgB.ID(), "jev", "jev-latest", "", 100, 200)
	jev.SetKind(model.ModelKindDecision)
	vectors := model.NewLLMModel(model.NewProviderID(), orgB.ID(), "vectors", "text-embedding-3-small", "", 100, 0)
	vectors.SetKind(model.ModelKindEmbeddings)
	team := model.NewTeam(orgA.ID(), "research", "Research", "")
	user := model.NewUser(testTenantID, "oidc", "human-1", "alice@example.com", "Alice", true, "user")

	vm := model.NewPersonalVirtualModel(user.ID(), "mine", "")
	vm.SetTeamID(team.ID())

	inTeam := []rbac.TeamAccess{{TeamID: string(team.ID()), Slug: team.Slug(), Grants: []rbac.ModelGrant{{ModelID: string(granted.ID()), Kind: rbac.ModelKindLLM}}}}

	cases := []struct {
		name   string
		vm     string
		teamsA []rbac.TeamAccess
		want   []string
	}{
		{
			name:   "without a virtual model every usable model is offered",
			teamsA: inTeam,
			want:   []string{"acme/gpt-4o", "umbrella/claude"},
		},
		{
			name:   "the charged team's organization alone offers models",
			vm:     string(vm.ID()),
			teamsA: inTeam,
			want:   []string{"acme/gpt-4o"},
		},
		{
			name: "a charged team the owner left offers nothing",
			vm:   string(vm.ID()),
			want: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orgStore := &fakeOrgStoreForModels{
				orgsByID: map[string]model.Organization{
					string(orgA.ID()): orgA,
					string(orgB.ID()): orgB,
				},
				memberships: map[string][]model.Membership{
					string(user.ID()): {model.NewMembership(user.ID(), orgA.ID()), model.NewMembership(user.ID(), orgB.ID())},
				},
			}
			providerStore := &fakeProviderStoreForPicker{fakeProviderStoreForModels{
				enabledModels: map[string][]model.LLMModel{
					string(orgA.ID()): {granted, ungranted},
					string(orgB.ID()): {other, jev, vectors},
				},
			}}
			h := api.NewHandler(providerStore, orgStore,
				&fakeVirtualModelStoreForModels{models: map[string][]model.VirtualModel{}},
				&fakePersonalVMStore{vms: []model.PersonalVirtualModel{vm}},
				nil, nil, nil, nil)

			target := "/api/personal-models/pipeline-models"
			if tc.vm != "" {
				target += "?vm=" + tc.vm
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			ctx := httpCtx.SetUser(req.Context(), user)
			ctx = httpCtx.SetPermissionResolver(ctx, func(ctx context.Context, orgID model.OrgID) (rbac.PermissionSet, error) {
				if orgID == orgA.ID() {
					return rbac.NewPermissionSet(nil, tc.teamsA), nil
				}
				// The organization-wide access of another organization does
				// not let it pay for a team it does not own.
				return rbac.NewPermissionSet([]string{string(rbac.PermModelUseOrg)}, nil), nil
			})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req.WithContext(ctx))

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var options []struct {
				ProxyName string `json:"proxyName"`
				Kind      string `json:"kind"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &options); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			got := []string{}
			for _, o := range options {
				if o.Kind == "model" {
					got = append(got, o.ProxyName)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("unexpected models\nwant: %v\ngot:  %v", tc.want, got)
			}
		})
	}
}

// A store error on the virtual model fails the picker: read as no virtual
// model, it would offer the models of every organization. An unknown one
// offers them, as without a virtual model.
func TestPersonalPipelineModelsReportStoreErrors(t *testing.T) {
	user := model.NewUser(testTenantID, "oidc", "human-1", "alice@example.com", "Alice", true, "user")
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"store error", errors.New("store unavailable"), http.StatusInternalServerError},
		{"unknown virtual model", port.ErrNotFound, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := api.NewHandler(&fakeProviderStoreForPicker{fakeProviderStoreForModels{}},
				&fakeOrgStoreForModels{orgsByID: map[string]model.Organization{}, memberships: map[string][]model.Membership{}},
				&fakeVirtualModelStoreForModels{models: map[string][]model.VirtualModel{}},
				&fakePersonalVMStore{err: tc.err},
				nil, nil, nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/api/personal-models/pipeline-models?vm=some-id", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req.WithContext(httpCtx.SetUser(req.Context(), user)))
			if rec.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

// fakePersonalVMStore exposes the PersonalVirtualModelStore reads of the
// picker; the rest panic loudly.
type fakePersonalVMStore struct {
	port.PersonalVirtualModelStore
	vms []model.PersonalVirtualModel
	err error
}

func (s *fakePersonalVMStore) GetPersonalVirtualModelByID(ctx context.Context, id model.PersonalVirtualModelID) (model.PersonalVirtualModel, error) {
	if s.err != nil {
		return nil, s.err
	}
	for _, vm := range s.vms {
		if vm.ID() == id {
			return vm, nil
		}
	}
	return nil, port.ErrNotFound
}

func (s *fakePersonalVMStore) ListPersonalVirtualModels(ctx context.Context, userID model.UserID) ([]model.PersonalVirtualModel, error) {
	var vms []model.PersonalVirtualModel
	for _, vm := range s.vms {
		if vm.UserID() == userID {
			vms = append(vms, vm)
		}
	}
	return vms, nil
}

// fakeProviderStoreForPicker adds the provider lookup the picker groups its
// models with.
type fakeProviderStoreForPicker struct {
	fakeProviderStoreForModels
}

func (s *fakeProviderStoreForPicker) GetProviderByID(ctx context.Context, id model.ProviderID) (model.Provider, error) {
	return nil, port.ErrNotFound
}
