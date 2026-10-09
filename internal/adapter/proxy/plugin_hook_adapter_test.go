package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/pipeline"
)

func TestApplyModifiedMessages_OpenAI(t *testing.T) {
	ec := pipeline.ExecutionContext{
		MessagesJSON: `[{"role":"user","content":"hi"}]`,
	}
	forwardExec := &pipeline.ForwardExecution{
		FinalMessagesJSON: `[{"role":"system","content":"injected"},{"role":"user","content":"hi"}]`,
	}
	req := &genaiProxy.ProxyRequest{Type: genaiProxy.RequestTypeChatCompletion}

	applyModifiedMessages(context.Background(), req, ec, forwardExec)

	if len(req.ChatOptions) != 1 {
		t.Fatalf("expected ChatOptions to be appended, got %d", len(req.ChatOptions))
	}

	opts := llm.NewChatCompletionOptions(req.ChatOptions...)
	if len(opts.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(opts.Messages))
	}
	if opts.Messages[0].Role() != llm.RoleSystem {
		t.Errorf("first message role = %q, want system", opts.Messages[0].Role())
	}
	if opts.Messages[0].Content() != "injected" {
		t.Errorf("first message content = %q, want injected", opts.Messages[0].Content())
	}
}

func TestApplyModifiedMessages_Anthropic(t *testing.T) {
	ec := pipeline.ExecutionContext{
		MessagesJSON: `[{"role":"user","content":"hi"}]`,
	}
	forwardExec := &pipeline.ForwardExecution{
		FinalMessagesJSON: `[{"role":"system","content":"injected"},{"role":"user","content":"hi"}]`,
	}
	req := &genaiProxy.ProxyRequest{Type: genaiProxy.RequestTypeMessage}

	applyModifiedMessages(context.Background(), req, ec, forwardExec)

	opts := llm.NewChatCompletionOptions(req.ChatOptions...)
	if len(opts.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(opts.Messages))
	}
	if opts.Messages[0].Role() != llm.RoleSystem {
		t.Errorf("first message role = %q, want system", opts.Messages[0].Role())
	}
}

func TestApplyModifiedMessages_NoChange(t *testing.T) {
	ec := pipeline.ExecutionContext{
		MessagesJSON: `[{"role":"user","content":"hi"}]`,
	}
	forwardExec := &pipeline.ForwardExecution{
		FinalMessagesJSON: ec.MessagesJSON,
	}
	req := &genaiProxy.ProxyRequest{Type: genaiProxy.RequestTypeChatCompletion}

	applyModifiedMessages(context.Background(), req, ec, forwardExec)

	if len(req.ChatOptions) != 0 {
		t.Errorf("expected no ChatOptions appended, got %d", len(req.ChatOptions))
	}
}

func TestApplyModifiedMessages_ConversionError(t *testing.T) {
	ec := pipeline.ExecutionContext{
		MessagesJSON: `[{"role":"user","content":"hi"}]`,
	}
	forwardExec := &pipeline.ForwardExecution{
		FinalMessagesJSON: `not json`,
	}
	req := &genaiProxy.ProxyRequest{Type: genaiProxy.RequestTypeChatCompletion}

	applyModifiedMessages(context.Background(), req, ec, forwardExec)

	if len(req.ChatOptions) != 0 {
		t.Errorf("expected no ChatOptions appended on conversion error, got %d", len(req.ChatOptions))
	}
}

func TestApplyModifiedMessages_AnthropicCarriesTopLevelSystem(t *testing.T) {
	ec := pipeline.ExecutionContext{
		MessagesJSON: `[{"role":"user","content":"hi"}]`,
	}
	forwardExec := &pipeline.ForwardExecution{
		FinalMessagesJSON: `[{"role":"system","content":"injected"},{"role":"user","content":"hi"}]`,
	}
	req := &genaiProxy.ProxyRequest{
		Type: genaiProxy.RequestTypeMessage,
		Body: []byte(`{"model":"m","system":"client policy","messages":[{"role":"user","content":"hi"}]}`),
	}

	applyModifiedMessages(context.Background(), req, ec, forwardExec)

	opts := llm.NewChatCompletionOptions(req.ChatOptions...)
	if len(opts.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(opts.Messages))
	}
	if opts.Messages[0].Role() != llm.RoleSystem || opts.Messages[0].Content() != "client policy" {
		t.Errorf("first message = (%q, %q), want the client system prompt", opts.Messages[0].Role(), opts.Messages[0].Content())
	}
	if opts.Messages[1].Content() != "injected" {
		t.Errorf("second message = %q, want the injected instruction", opts.Messages[1].Content())
	}
}

func TestApplyModifiedMessages_AnthropicSystemBlocks(t *testing.T) {
	ec := pipeline.ExecutionContext{
		MessagesJSON: `[{"role":"user","content":"hi"}]`,
	}
	forwardExec := &pipeline.ForwardExecution{
		FinalMessagesJSON: `[{"role":"user","content":"bonjour"}]`,
	}
	req := &genaiProxy.ProxyRequest{
		Type: genaiProxy.RequestTypeMessage,
		Body: []byte(`{"model":"m","system":[{"type":"text","text":"first"},{"type":"text","text":"second","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`),
	}

	applyModifiedMessages(context.Background(), req, ec, forwardExec)

	opts := llm.NewChatCompletionOptions(req.ChatOptions...)
	if len(opts.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(opts.Messages))
	}
	if opts.Messages[0].Content() != "first" || opts.Messages[1].Content() != "second" {
		t.Fatalf("system blocks = (%q, %q), want (first, second)", opts.Messages[0].Content(), opts.Messages[1].Content())
	}
	if opts.Messages[0].Role() != llm.RoleSystem || opts.Messages[1].Role() != llm.RoleSystem {
		t.Errorf("system block roles = (%q, %q), want system", opts.Messages[0].Role(), opts.Messages[1].Role())
	}

	// A client that set a cache breakpoint on its system prompt keeps it.
	cached, ok := opts.Messages[1].(llm.CacheControlMessage)
	if !ok {
		t.Fatal("a system block should implement CacheControlMessage")
	}
	cc := cached.CacheControl()
	if cc == nil || cc.Type != "ephemeral" {
		t.Fatalf("cache control = %+v, want ephemeral", cc)
	}
	if cc.TTL == nil || *cc.TTL != "1h" {
		t.Errorf("cache control ttl = %v, want 1h", cc.TTL)
	}

	// And a block without one does not get invented a breakpoint.
	if first, ok := opts.Messages[0].(llm.CacheControlMessage); ok && first.CacheControl() != nil {
		t.Errorf("first block carries no cache_control, got %+v", first.CacheControl())
	}
}

func TestRequestSystemMessages_Absent(t *testing.T) {
	for name, body := range map[string]string{
		"empty body":   "",
		"no system":    `{"model":"m","messages":[]}`,
		"null system":  `{"model":"m","system":null}`,
		"empty string": `{"model":"m","system":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			msgs, err := requestSystemMessages([]byte(body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(msgs) != 0 {
				t.Errorf("messages = %d, want 0", len(msgs))
			}
		})
	}
}

func TestRequestSystemMessages_UnsupportedType(t *testing.T) {
	if _, err := requestSystemMessages([]byte(`{"system":42}`)); err == nil {
		t.Error("expected an error for a numeric system prompt")
	}
}

// --- Regression tests for issue #34 -----------------------------------------
//
// ExecutionContext used to declare both RequestJSON and BodyJSON carrying the
// same semantic ("the raw LLM request body"), but only BodyJSON was populated
// by buildEC / buildMiddlewareEC. The downstream consumers (generator node's
// "request" output, PreRequest plugins' Model field, script-processor's
// ctx.request) therefore read "" at runtime, while tests passed because they
// seeded RequestJSON by hand through the harness. Going through buildEC — not
// the harness — is what makes the tests below catch a future regression.

// stubVM satisfies model.VirtualModel with just an ID, which is all buildEC
// reads off it (it stores vm.ID() in VisitedVMs).
type stubVM struct {
	id model.VirtualModelID
}

func (s *stubVM) ID() model.VirtualModelID    { return s.id }
func (s *stubVM) EntityID() string            { return string(s.id) }
func (s *stubVM) OrgID() model.OrgID          { return "" }
func (s *stubVM) Name() string                { return "" }
func (s *stubVM) Description() string         { return "" }
func (s *stubVM) Graph() *model.PipelineGraph { return nil }
func (s *stubVM) CatalogOverrides() *model.CatalogOverrides { return nil }
func (s *stubVM) CreatedAt() time.Time        { return time.Time{} }
func (s *stubVM) UpdatedAt() time.Time        { return time.Time{} }

// newAdapterForBuildEC builds a PipelineHookAdapter with just enough fields
// wired for buildEC to run without touching stores (no org, no OrgID in ctx
// → no store lookups).
func newAdapterForBuildEC() *PipelineHookAdapter {
	return &PipelineHookAdapter{}
}

// stubOrg satisfies model.Organization with just an ID, which is all
// buildMiddlewareEC reads off it.
func stubOrg() model.Organization {
	return model.NewOrganization("", "stub", "Stub", "")
}

func TestBuildEC_PopulatesBodyJSON(t *testing.T) {
	adapter := newAdapterForBuildEC()

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"temperature":0.7}`)

	ec := adapter.buildEC(context.Background(), &genaiProxy.ProxyRequest{
		Body:  body,
		Type:  genaiProxy.RequestTypeChatCompletion,
		Model: "m",
	}, nil, &stubVM{id: "vm-1"})

	if ec.BodyJSON != string(body) {
		t.Fatalf("BodyJSON = %q, want %q", ec.BodyJSON, string(body))
	}
}

// TestBuildEC_PopulatesBodyJSON_PersonalVMPath covers the personal-VM branch of
// buildEC: org == nil but OrgIDFromContext(ctx) != "". In production this
// happens when a personal virtual model is resolved through XoloAuthExtractor;
// buildEC then derives orgID from the token's org and tries to populate
// protoModels/protoVMs. With nil stores those stay nil, but the BodyJSON seed
// must still happen — and the branch itself must not panic.
func TestBuildEC_PopulatesBodyJSON_PersonalVMPath(t *testing.T) {
	adapter := newAdapterForBuildEC()

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	ctx := context.WithValue(context.Background(), contextKeyOrgID, "org-from-token")

	ec := adapter.buildEC(ctx, &genaiProxy.ProxyRequest{
		Body:  body,
		Type:  genaiProxy.RequestTypeChatCompletion,
		Model: "m",
	}, nil, &stubVM{id: "vm-personal"})

	if ec.OrgID != "org-from-token" {
		t.Errorf("OrgID = %q, want %q (derived from ctx)", ec.OrgID, "org-from-token")
	}
	if ec.BodyJSON != string(body) {
		t.Fatalf("BodyJSON = %q, want %q", ec.BodyJSON, string(body))
	}
}

func TestBuildMiddlewareEC_PopulatesBodyJSON(t *testing.T) {
	adapter := newAdapterForBuildEC()

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	ec := adapter.buildMiddlewareEC(context.Background(), &genaiProxy.ProxyRequest{
		Body:  body,
		Type:  genaiProxy.RequestTypeChatCompletion,
		Model: "m",
	}, stubOrg())

	if ec.BodyJSON != string(body) {
		t.Fatalf("BodyJSON = %q, want %q", ec.BodyJSON, string(body))
	}
}

// Sanity check: GeneratorExecutor.Forward exposes ec.BodyJSON as the
// "request" output port — that is the value the engine seeds into the
// generator node's ValueContext and what downstream nodes receive.
func TestGeneratorExecutor_RequestPortCarriesBodyJSON(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	ec := pipeline.ExecutionContext{BodyJSON: string(body)}

	out, err := pipeline.NewGeneratorExecutor().Forward(
		context.Background(),
		model.PipelineNode{ID: "g", Type: model.NodeTypeGenerator},
		nil, ec,
	)
	if err != nil {
		t.Fatalf("generator.Forward: %v", err)
	}
	got, _ := out.OutputValues["request"].(string)
	if got != string(body) {
		t.Fatalf("generator request output = %q, want %q", got, string(body))
	}
}

func TestBuildEC_SecretScopeFollowsGraphOwner(t *testing.T) {
	adapter := newAdapterForBuildEC()
	personal := model.NewPersonalVirtualModel("owner", "personal", "")
	org := model.NewOrganization("tenant", "org", "Org", "")
	vm := model.NewVirtualModel(org.ID(), "virtual", "")
	req := &genaiProxy.ProxyRequest{}
	for _, orgID := range []string{"org-a", "org-b"} {
		ctx := context.WithValue(t.Context(), contextKeyOrgID, orgID)
		ec := adapter.buildEC(ctx, req, nil, &personalVMAdapter{pvm: personal})
		require.Equal(t, "~:owner", ec.SecretScopeID)
		require.Equal(t, orgID, ec.OrgID)
		ec = adapter.buildEC(ctx, req, org, vm)
		require.Equal(t, string(org.ID()), ec.SecretScopeID)
		ec = adapter.buildMiddlewareEC(ctx, req, org)
		require.Equal(t, string(org.ID()), ec.SecretScopeID)
	}
}
