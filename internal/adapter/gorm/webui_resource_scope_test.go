package gorm_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/adapter/events"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/crypto"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui"
	"github.com/xolo-gateway/xolo/internal/http/middleware/memberships"
)

const scopeTestSecretKey = "0123456789abcdef0123456789abcdef"

type scopeHTTPObserver struct {
	port.QuotaStore
	events                  []model.Event
	quotaReads, quotaWrites int
}

func (s *scopeHTTPObserver) Emit(_ context.Context, e model.Event) { s.events = append(s.events, e) }
func (s *scopeHTTPObserver) GetQuota(ctx context.Context, scope model.QuotaScope, id string) (model.Quota, error) {
	s.quotaReads++
	return s.QuotaStore.GetQuota(ctx, scope, id)
}
func (s *scopeHTTPObserver) SetQuota(ctx context.Context, q model.Quota) error {
	s.quotaWrites++
	return s.QuotaStore.SetQuota(ctx, q)
}

type scopeHTTPFixture struct {
	org      model.Organization
	provider model.Provider
	llm      model.LLMModel
	vm       model.VirtualModel
	mw       model.Middleware
	invite   model.InviteToken
	member   model.Membership
}

func newScopeHTTPFixture(t *testing.T, store *xologorm.Store, org model.Organization, baseURL string) scopeHTTPFixture {
	t.Helper()
	ctx := t.Context()
	key, err := crypto.Encrypt(scopeTestSecretKey, "PRIVATE-API-KEY")
	if err != nil {
		t.Fatal(err)
	}
	p := model.NewProvider(org.ID(), "PRIVATE-PROVIDER", "openai", baseURL, key, "EUR")
	suffix := string(p.ID())
	llm := model.NewLLMModel(p.ID(), org.ID(), "private-model-"+suffix, "real", "PRIVATE-MODEL", 0, 0)
	vm := model.NewVirtualModel(org.ID(), "private-vm-"+suffix, "PRIVATE-VM")
	mw := model.NewMiddleware(org.ID(), "private-mw-"+suffix, "PRIVATE-MIDDLEWARE")
	user := model.NewUser(org.TenantID(), "test", suffix, "private-"+suffix+"@example.test", "PRIVATE-MEMBER", true)
	invite := model.NewInviteToken(org.ID(), model.RoleMember, nil, nil, nil, user.ID())
	member := model.NewMembership(user.ID(), org.ID())
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.CreateProvider(ctx, p))
	must(store.CreateLLMModel(ctx, llm))
	must(store.CreateVirtualModel(ctx, vm))
	must(store.CreateMiddleware(ctx, mw))
	must(store.SaveUser(ctx, user))
	must(store.CreateInvite(ctx, invite))
	must(store.AddMember(ctx, member))
	daily := int64(7654321)
	must(store.SetQuota(ctx, model.NewQuota(model.QuotaScopeUser, string(user.ID()), "EUR", &daily, nil, nil)))
	return scopeHTTPFixture{org, p, llm, vm, mw, invite, member}
}

// Snapshot independently loaded objects and collection membership. This detects
// writes, deletions, new rows and changes to any persisted fields after denial.
func scopeHTTPSnapshot(t *testing.T, store *xologorm.Store, f scopeHTTPFixture, orgs []model.Organization) []any {
	t.Helper()
	ctx := t.Context()
	var state []any
	for _, org := range orgs {
		p, err := store.ListProviders(ctx, org.ID())
		if err != nil {
			t.Fatal(err)
		}
		llms, err := store.ListLLMModels(ctx, org.ID())
		if err != nil {
			t.Fatal(err)
		}
		vms, err := store.ListVirtualModels(ctx, org.ID())
		if err != nil {
			t.Fatal(err)
		}
		mws, err := store.ListMiddlewares(ctx, org.ID())
		if err != nil {
			t.Fatal(err)
		}
		invites, err := store.ListInvites(ctx, org.ID())
		if err != nil {
			t.Fatal(err)
		}
		state = append(state, p, llms, vms, mws, invites)
	}
	member, err := store.GetMembership(ctx, f.member.ID())
	if err != nil {
		t.Fatal(err)
	}
	quota, err := store.GetQuota(ctx, model.QuotaScopeUser, string(f.member.UserID()))
	if err != nil {
		t.Fatal(err)
	}
	return append(state, member, quota)
}

// Exercise the real WebUI mux, permission resolver, event decorators and stores.
// eachBackend runs SQLite normally and adds PostgreSQL with -tags integration.
func TestWebUIResourceIsolation(t *testing.T) {
	eachBackend(t, func(t *testing.T, store *xologorm.Store) {
		ctx := t.Context()
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		tenant := model.NewTenant("scope-a", "A", "")
		otherTenant := model.NewTenant("scope-b", "B", "")
		must(store.CreateTenant(ctx, tenant))
		must(store.CreateTenant(ctx, otherTenant))
		own := model.NewOrganization(tenant.ID(), "shared", "Own", "", "EUR")
		other := model.NewOrganization(tenant.ID(), "other", "Other", "", "EUR")
		foreign := model.NewOrganization(otherTenant.ID(), own.Slug(), "Other tenant", "", "EUR")
		orgs := []model.Organization{own, other, foreign}
		for _, org := range orgs {
			must(store.CreateOrg(ctx, org))
		}
		actor := model.NewUser(tenant.ID(), "test", "actor", "actor@example.test", "Actor", true, model.PlatformRoleUser)
		admin := model.NewUser(tenant.ID(), "test", "admin", "admin@example.test", "Admin", true, model.PlatformRoleAdmin)
		outsider := model.NewUser(tenant.ID(), "test", "outsider", "outsider@example.test", "Outsider", true, model.PlatformRoleUser)
		for _, user := range []model.User{actor, admin, outsider} {
			must(store.SaveUser(ctx, user))
		}
		actorMember := model.NewMembership(actor.ID(), own.ID())
		must(store.AddMember(ctx, actorMember))
		must(store.EnsureBuiltinRoles(ctx, own.ID()))
		roles, err := store.ListOrgRoles(ctx, own.ID())
		must(err)
		assigned := false
		for _, role := range roles {
			if role.BuiltinKind() == model.BuiltinKindOwner {
				must(store.SetMembershipRoles(ctx, actorMember.ID(), []model.RoleID{role.ID()}))
				assigned = true
			}
		}
		if !assigned {
			t.Fatal("owner role not found")
		}
		var networkCalls atomic.Int64
		providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			networkCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "/models") {
				fmt.Fprint(w, `{"object":"list","data":[{"id":"PRIVATE-UPSTREAM","object":"model","created":0,"owned_by":"test"}]}`)
				return
			}
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
		}))
		defer providerServer.Close()
		observer := &scopeHTTPObserver{QuotaStore: store}
		handler := memberships.Middleware(store, store)(webui.NewHandler(
			nil, store, store, store, events.NewProviderStore(store, observer),
			events.NewVirtualModelStore(store, observer), events.NewMiddlewareStore(store, observer), store,
			store, events.NewInviteStore(store, observer), service.NewInvitationService(events.NewInvitationTransaction(store, observer)), store, observer, nil, nil, store, scopeTestSecretKey,
			nil, nil, nil, store, store, store, store, 100, 100,
		))
		operations := []struct {
			name, family, method, path, body string
			status                           int
			eventType                        string
		}{
			{"provider edit", "provider", "GET", "providers/{providerID}/edit", "", 200, ""},
			{"provider update", "provider", "POST", "providers/{providerID}/edit", "name=changed&provider_type=openai&base_url=http%3A%2F%2Fexample.test&currency=EUR&active=on", 303, model.EventTypeProviderUpdated},
			{"provider delete", "provider", "DELETE", "providers/{providerID}", "", 303, model.EventTypeProviderDeleted},
			{"provider test", "provider", "POST", "providers/{providerID}/test", "", 200, ""},
			{"models list", "provider", "GET", "providers/{providerID}/models", "", 200, ""},
			{"model new", "provider", "GET", "providers/{providerID}/models/new", "", 200, ""},
			{"model create", "provider", "POST", "providers/{providerID}/models", "proxy_name=changed-{providerID}&real_model=changed&enabled=on", 303, model.EventTypeModelCreated},
			{"model import page", "provider", "GET", "providers/{providerID}/models/import", "", 200, ""},
			{"model import", "provider", "POST", "providers/{providerID}/models/import", "proxy_prefix=imported-{providerID}-&models=PRIVATE-UPSTREAM", 303, model.EventTypeModelCreated},
			{"model import preview", "provider", "POST", "providers/{providerID}/models/import/preview", "proxy_prefix=preview-&candidate=PRIVATE-UPSTREAM", 200, ""},
			{"model edit", "model", "GET", "providers/{providerID}/models/{modelID}/edit", "", 200, ""},
			{"model update", "model", "POST", "providers/{providerID}/models/{modelID}/edit", "proxy_name=changed-{providerID}&real_model=changed&enabled=on", 303, model.EventTypeModelUpdated},
			{"model delete", "model", "DELETE", "providers/{providerID}/models/{modelID}", "", 303, model.EventTypeModelDeleted},
			{"virtual edit", "virtual", "GET", "virtual-models/{modelID}/edit", "", 200, ""},
			{"virtual update", "virtual", "POST", "virtual-models/{modelID}/edit", "name=changed-{modelID}&description=changed&enabled=on", 303, model.EventTypeVirtualModelUpdated},
			{"virtual delete", "virtual", "DELETE", "virtual-models/{modelID}", "", 303, model.EventTypeVirtualModelDeleted},
			{"virtual pipeline", "virtual", "GET", "virtual-models/{modelID}/pipeline", "", 200, ""},
			{"middleware edit", "middleware", "GET", "middlewares/{middlewareID}/edit", "", 200, ""},
			{"middleware update", "middleware", "POST", "middlewares/{middlewareID}/edit", "name=changed-{modelID}&description=changed&enabled=on", 303, model.EventTypeMiddlewareUpdated},
			{"middleware toggle", "middleware", "POST", "middlewares/{middlewareID}/toggle", "", 303, model.EventTypeMiddlewareUpdated},
			{"middleware delete", "middleware", "DELETE", "middlewares/{middlewareID}", "", 303, model.EventTypeMiddlewareDeleted},
			{"middleware pipeline", "middleware", "GET", "middlewares/{middlewareID}/pipeline", "", 200, ""},
			{"invite revoke", "invite", "DELETE", "invites/{inviteID}", "", 303, ""},
			{"invite delete", "invite", "DELETE", "invites/{inviteID}/delete", "", 303, model.EventTypeInviteDeleted},
			{"quota read", "quota", "GET", "members/{membershipID}/quota", "", 200, ""},
			{"quota save", "quota", "POST", "members/{membershipID}/quota", "daily_budget=10", 303, ""},
		}
		for _, op := range operations {
			t.Run(op.name, func(t *testing.T) {
				scenarios := []string{"authorized", "same tenant", "other tenant same slug", "unknown", "missing org", "no permission"}
				if op.family == "model" {
					scenarios = append(scenarios, "wrong provider", "foreign model", "inconsistent model org", "foreign provider", "unknown provider")
				}
				for _, scenario := range scenarios {
					t.Run(scenario, func(t *testing.T) {
						for _, platformAdmin := range []bool{false, true} {
							if scenario == "no permission" && platformAdmin {
								continue
							}
							t.Run(fmt.Sprintf("admin=%v", platformAdmin), func(t *testing.T) {
								targetOrg := own
								if scenario == "same tenant" || scenario == "foreign provider" {
									targetOrg = other
								}
								if scenario == "other tenant same slug" {
									targetOrg = foreign
								}
								f := newScopeHTTPFixture(t, store, targetOrg, providerServer.URL)
								providerID := string(f.provider.ID())
								if op.family == "model" {
									switch scenario {
									case "wrong provider", "foreign model", "inconsistent model org":
										modelOrg, modelProvider := own.ID(), f.provider.ID()
										if scenario == "wrong provider" {
											second := model.NewProvider(own.ID(), "Second", "openai", providerServer.URL, "key", "EUR")
											must(store.CreateProvider(t.Context(), second))
											modelProvider = second.ID()
										} else if scenario == "foreign model" {
											second := model.NewProvider(other.ID(), "Foreign", "openai", providerServer.URL, "key", "EUR")
											must(store.CreateProvider(t.Context(), second))
											modelProvider, modelOrg = second.ID(), other.ID()
										} else {
											modelOrg = other.ID()
										}
										m := model.NewLLMModel(modelProvider, modelOrg, "mismatch-"+providerID, "real", "PRIVATE-MODEL", 0, 0)
										must(store.CreateLLMModel(t.Context(), m))
										f.llm = m
									case "unknown provider":
										providerID = "unknown"
									}
								}
								modelID := string(f.llm.ID())
								if op.family == "virtual" {
									modelID = string(f.vm.ID())
								}
								values := map[string]string{
									"providerID": providerID, "modelID": modelID, "middlewareID": string(f.mw.ID()),
									"inviteID": string(f.invite.ID()), "membershipID": string(f.member.ID()),
								}
								if scenario == "unknown" {
									key := map[string]string{"provider": "providerID", "model": "modelID", "virtual": "modelID", "middleware": "middlewareID", "invite": "inviteID", "quota": "membershipID"}[op.family]
									values[key] = "unknown"
								}
								path, body := op.path, op.body
								for k, v := range values {
									path = strings.ReplaceAll(path, "{"+k+"}", v)
									body = strings.ReplaceAll(body, "{"+k+"}", v)
								}
								slug := own.Slug()
								if scenario == "missing org" {
									slug = "unknown"
								}
								request := httptest.NewRequest(op.method, "/orgs/"+slug+"/admin/"+path, strings.NewReader(body))
								request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
								user := model.User(actor)
								if platformAdmin {
									user = admin
								}
								if scenario == "no permission" {
									user = outsider
								}
								reqCtx := httpCtx.SetTenant(request.Context(), tenant)
								reqCtx = httpCtx.SetUser(reqCtx, user)
								reqCtx = httpCtx.SetBaseURL(reqCtx, "http://gateway.example.test")
								request = request.WithContext(reqCtx)
								before := scopeHTTPSnapshot(t, store, f, orgs)
								observer.events = nil
								observer.quotaReads = 0
								observer.quotaWrites = 0
								networkBefore := networkCalls.Load()
								rr := httptest.NewRecorder()
								handler.ServeHTTP(rr, request)
								want := http.StatusNotFound
								if scenario == "authorized" {
									want = op.status
								}
								if scenario == "no permission" {
									want = http.StatusForbidden
								}
								if rr.Code != want {
									t.Fatalf("status = %d, want %d: %s", rr.Code, want, rr.Body.String())
								}
								if scenario != "authorized" {
									if rr.Body.String() != http.StatusText(want)+"\n" {
										t.Fatalf("non-generic denial: %q", rr.Body.String())
									}
									after := scopeHTTPSnapshot(t, store, f, orgs)
									if !reflect.DeepEqual(before, after) {
										t.Fatal("persistent state changed after denial")
									}
									if len(observer.events) != 0 || observer.quotaReads != 0 || observer.quotaWrites != 0 || networkCalls.Load() != networkBefore {
										t.Fatalf("effects after denial: events=%d quota=%d/%d network=%d", len(observer.events), observer.quotaReads, observer.quotaWrites, networkCalls.Load()-networkBefore)
									}
									return
								}
								if op.eventType == "" {
									if len(observer.events) != 0 {
										t.Fatalf("unexpected events: %v", observer.events)
									}
								} else if len(observer.events) != 1 || observer.events[0].Type() != op.eventType || observer.events[0].OrgID() != own.ID() {
									t.Fatalf("authorized mutation lost its event: %v", observer.events)
								}
								assertScopeHTTPSuccess(t, store, f, op.name)
								if op.status == http.StatusOK {
									wantText := ""
									switch op.name {
									case "provider edit", "model new":
										wantText = f.provider.Name()
									case "models list", "model edit":
										wantText = f.llm.ProxyName()
									case "virtual edit", "virtual pipeline":
										wantText = f.vm.Name()
									case "middleware edit", "middleware pipeline":
										wantText = f.mw.Name()
									case "quota read":
										wantText = "PRIVATE-MEMBER"
									case "provider test":
										wantText = "Connection successful"
									case "model import page":
										wantText = "PRIVATE-UPSTREAM"
									case "model import preview":
										wantText = "preview-PRIVATE-UPSTREAM"
									}
									if wantText == "" || !strings.Contains(rr.Body.String(), wantText) {
										t.Fatalf("authorized response does not contain %q", wantText)
									}
								}
							})
						}
					})
				}
			})
		}
	})
}

func assertScopeHTTPSuccess(t *testing.T, store *xologorm.Store, f scopeHTTPFixture, operation string) {
	t.Helper()
	ctx := t.Context()
	switch operation {
	case "provider update":
		p, err := store.GetProviderByID(ctx, f.provider.ID())
		if err != nil {
			t.Fatal(err)
		}
		if p.Name() != "changed" || p.OrgID() != f.org.ID() {
			t.Fatal("provider update was not persisted in its org")
		}
	case "provider delete":
		if _, err := store.GetProviderByID(ctx, f.provider.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("provider deletion: %v", err)
		}
	case "model create", "model update":
		m, err := store.GetLLMModelByProxyName(ctx, f.org.ID(), "changed-"+string(f.provider.ID()))
		if err != nil {
			t.Fatal(err)
		}
		if m.OrgID() != f.org.ID() || m.ProviderID() != f.provider.ID() {
			t.Fatal("model parents do not match the validated URL")
		}
		if operation == "model update" && m.ID() != f.llm.ID() {
			t.Fatal("wrong model updated")
		}
	case "model import":
		// Imported models start disabled, which GetLLMModelByProxyName skips.
		models, err := store.ListLLMModels(ctx, f.org.ID())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, m := range models {
			if m.ProxyName() == "imported-"+string(f.provider.ID())+"-PRIVATE-UPSTREAM" {
				found = m.OrgID() == f.org.ID() && m.ProviderID() == f.provider.ID()
			}
		}
		if !found {
			t.Fatal("imported model is missing from the validated provider and org")
		}
	case "model delete":
		if _, err := store.GetLLMModelByID(ctx, f.llm.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("model deletion: %v", err)
		}
	case "virtual update":
		vm, err := store.GetVirtualModelByID(ctx, f.vm.ID())
		if err != nil {
			t.Fatal(err)
		}
		if vm.Description() != "changed" {
			t.Fatal("virtual model not updated")
		}
	case "virtual delete":
		if _, err := store.GetVirtualModelByID(ctx, f.vm.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("virtual model deletion: %v", err)
		}
	case "middleware update", "middleware toggle":
		mw, err := store.GetMiddlewareByID(ctx, f.mw.ID())
		if err != nil {
			t.Fatal(err)
		}
		if operation == "middleware update" && mw.Description() != "changed" {
			t.Fatal("middleware not updated")
		}
		if operation == "middleware toggle" && mw.Enabled() {
			t.Fatal("middleware not disabled")
		}
	case "middleware delete":
		if _, err := store.GetMiddlewareByID(ctx, f.mw.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("middleware deletion: %v", err)
		}
	case "invite revoke":
		invite, err := store.GetInviteByID(ctx, f.invite.ID())
		if err != nil {
			t.Fatal(err)
		}
		if invite.RevokedAt() == nil {
			t.Fatal("invitation not revoked")
		}
	case "invite delete":
		if _, err := store.GetInviteByID(ctx, f.invite.ID()); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("invitation deletion: %v", err)
		}
	case "quota save":
		q, err := store.GetQuota(ctx, model.QuotaScopeUser, string(f.member.UserID()))
		if err != nil {
			t.Fatal(err)
		}
		if q.DailyBudget() == nil || *q.DailyBudget() != 10000000 {
			t.Fatal("member budget not saved")
		}
	}
}
