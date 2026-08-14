package events_test

import (
	"context"
	"testing"

	eventsAdapter "github.com/xolo-gateway/xolo/internal/adapter/events"
	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	gormpkg "gorm.io/gorm"
)

type captureEmitter struct {
	events []model.Event
}

func (c *captureEmitter) Emit(_ context.Context, e model.Event) {
	c.events = append(c.events, e)
}

func newStore(t *testing.T) *xologorm.Store {
	t.Helper()
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return xologorm.NewStore(db)
}

func withActor(ctx context.Context) context.Context {
	user := model.NewUser(testTenantID, "test", "sub-1", "u@example.com", "Alice", true, "user")
	return httpCtx.SetUser(ctx, user)
}

func TestProviderStore_EmitsWithActor(t *testing.T) {
	backend := newStore(t)
	emitter := &captureEmitter{}
	store := eventsAdapter.NewProviderStore(backend, emitter)

	ctx := withActor(context.Background())
	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := backend.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	p := model.NewProvider(org.ID(), "OpenAI", "openai", "https://api.openai.com", "enc", "USD")

	if err := store.CreateProvider(ctx, p); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if err := store.DeleteProvider(ctx, p.ID()); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}

	if len(emitter.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(emitter.events))
	}
	created := emitter.events[0]
	if created.Type() != model.EventTypeProviderCreated {
		t.Fatalf("expected %q, got %q", model.EventTypeProviderCreated, created.Type())
	}
	if created.OrgID() != org.ID() {
		t.Fatalf("expected %q, got %q", org.ID(), created.OrgID())
	}
	if created.Attributes()["actor"] != "Alice" {
		t.Fatalf("expected actor Alice, got %q", created.Attributes()["actor"])
	}
	if created.Attributes()["provider_name"] != "OpenAI" {
		t.Fatalf("expected provider_name OpenAI, got %q", created.Attributes()["provider_name"])
	}
	if emitter.events[1].Type() != model.EventTypeProviderDeleted {
		t.Fatalf("expected delete event, got %q", emitter.events[1].Type())
	}
	// The delete event must still carry the entity name (pre-fetched).
	if emitter.events[1].Attributes()["provider_name"] != "OpenAI" {
		t.Fatalf("expected deleted provider_name OpenAI, got %q", emitter.events[1].Attributes()["provider_name"])
	}
}

func TestProviderStore_NoActorNoEvent(t *testing.T) {
	backend := newStore(t)
	emitter := &captureEmitter{}
	store := eventsAdapter.NewProviderStore(backend, emitter)

	// No user in context (system/seed path): no event should be emitted.
	ctx := context.Background()
	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := backend.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	p := model.NewProvider(org.ID(), "OpenAI", "openai", "https://api.openai.com", "enc", "USD")
	if err := store.CreateProvider(ctx, p); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if len(emitter.events) != 0 {
		t.Fatalf("expected no events without an actor, got %d", len(emitter.events))
	}
}

func TestRoleStore_MemberUpdatedResolvesOrgAndUser(t *testing.T) {
	backend := newStore(t)
	emitter := &captureEmitter{}
	// The raw gorm store also serves as the membership resolver.
	store := eventsAdapter.NewRoleStore(backend, emitter, backend)

	ctx := withActor(context.Background())
	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := backend.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	user := model.NewUser(testTenantID, "test", "sub-2", "bob@example.com", "Bob", true, "user")
	user.SetPreferences(model.NewUserPreferences())
	if err := backend.SaveUser(ctx, user); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}
	membership := model.NewMembership(user.ID(), org.ID())
	if err := backend.AddMember(ctx, membership); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	if err := store.SetMembershipRoles(ctx, membership.ID(), nil); err != nil {
		t.Fatalf("SetMembershipRoles: %v", err)
	}

	if len(emitter.events) != 1 {
		t.Fatalf("expected 1 member.updated event, got %d", len(emitter.events))
	}
	ev := emitter.events[0]
	if ev.Type() != model.EventTypeMemberUpdated {
		t.Fatalf("expected %q, got %q", model.EventTypeMemberUpdated, ev.Type())
	}
	if ev.OrgID() != org.ID() {
		t.Fatalf("expected org %q, got %q", org.ID(), ev.OrgID())
	}
	if ev.Attributes()["member_user_id"] != string(user.ID()) {
		t.Fatalf("expected member_user_id %q, got %q", user.ID(), ev.Attributes()["member_user_id"])
	}
}

func TestApplicationStore_TokenEvents(t *testing.T) {
	backend := newStore(t)
	emitter := &captureEmitter{}
	store := eventsAdapter.NewApplicationStore(backend, emitter)

	ctx := withActor(context.Background())
	org := model.NewOrganization(testTenantID, "acme", "Acme", "")
	if err := backend.CreateOrg(ctx, org); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	app := model.NewApplication(org.ID(), "CLI", "", true)
	if err := backend.CreateApplication(ctx, app); err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}

	token := model.NewApplicationAuthToken(app, org.ID(), "prod", "secret-value", nil)
	if err := store.CreateApplicationAuthToken(ctx, token); err != nil {
		t.Fatalf("CreateApplicationAuthToken: %v", err)
	}
	if err := store.DeleteApplicationAuthToken(ctx, token.ID()); err != nil {
		t.Fatalf("DeleteApplicationAuthToken: %v", err)
	}

	if len(emitter.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(emitter.events))
	}
	if emitter.events[0].Type() != model.EventTypeApplicationTokenCreated {
		t.Fatalf("expected token created, got %q", emitter.events[0].Type())
	}
	del := emitter.events[1]
	if del.Type() != model.EventTypeApplicationTokenDeleted {
		t.Fatalf("expected token deleted, got %q", del.Type())
	}
	// The delete event must carry the pre-fetched label.
	if del.Attributes()["label"] != "prod" {
		t.Fatalf("expected label prod, got %q", del.Attributes()["label"])
	}
}

// testTenantID is the tenant every fixture of this package belongs to.
// Tenancy is not what these tests exercise: they only need a stable, shared
// owner so the tenant-scoped unique keys behave like the pre-tenant ones.
const testTenantID = model.TenantID("test-tenant")
