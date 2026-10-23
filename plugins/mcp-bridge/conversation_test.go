package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// newResourceMCPServer exposes the given resources and a single echo tool.
func newResourceMCPServer(t *testing.T, resources map[string]string) *httptest.Server {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echoes text"}, echo)

	for uri, text := range resources {
		text := text
		server.AddResource(
			&mcp.Resource{URI: uri, Name: uri, MIMEType: "text/plain"},
			func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
					URI: uri, MIMEType: "text/plain", Text: text,
				}}}, nil
			},
		)
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(func() {
		// A plugin under test keeps its MCP session open between requests; the
		// hanging notification stream it holds would make Close wait forever.
		srv.CloseClientConnections()
		srv.Close()
	})
	return srv
}

// newTestPlugin builds a plugin whose pooled sessions are closed when the test
// ends: keeping them open leaves the MCP server holding a stream, and the test's
// server teardown would wait on it.
func newTestPlugin(t *testing.T) *Plugin {
	t.Helper()
	p := &Plugin{}
	p.SetHostClient(newFakeHostClient())
	t.Cleanup(p.Close)
	return p
}

func requestContext(endpoint, cfg string) *proto.RequestContext {
	return &proto.RequestContext{
		OrgId:         "org-1",
		SecretScopeId: "org-1",
		NodeId:        "node-1",
		UserId:        "user-1",
		SessionId:     "session-1",
		ConfigJson:    `{"endpoint":"` + endpoint + `"` + cfg + `}`,
	}
}

// TestPlugin_ConfiguredResourceIsInjected is the second acceptance criterion: a
// configured MCP resource shows up in the context, through the conversation
// values the host injects before the model's first call.
func TestPlugin_ConfiguredResourceIsInjected(t *testing.T) {
	srv := newResourceMCPServer(t, map[string]string{
		"file:///contexte/procedure.md": "Ouvrir un ticket avant tout redémarrage.",
	})

	p := newTestPlugin(t)
	reqCtx := requestContext(srv.URL, `,"resources":["file:///contexte/procedure.md"]`)

	out, err := p.ListTools(context.Background(), &proto.ListToolsInput{Ctx: reqCtx})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	if len(out.ConversationValues) != 1 {
		t.Fatalf("expected the configured resource injected, got %v", out.ConversationValues)
	}
	value := out.ConversationValues[0]
	if value.GetContent() != "Ouvrir un ticket avant tout redémarrage." {
		t.Errorf("unexpected resource content: %q", value.GetContent())
	}
	if value.GetLabel() == "" {
		t.Error("expected the injected resource to carry a label")
	}
}

// TestPlugin_ResourceByServerName checks a resource named the way the server
// names it is resolved, like it is by URI.
func TestPlugin_ResourceByServerName(t *testing.T) {
	srv := newResourceMCPServer(t, map[string]string{"file:///readme.md": "the readme"})

	p := newTestPlugin(t)
	reqCtx := requestContext(srv.URL, `,"resources":["file:///readme.md"]`)

	out, err := p.ListTools(context.Background(), &proto.ListToolsInput{Ctx: reqCtx})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(out.ConversationValues) != 1 || out.ConversationValues[0].GetContent() != "the readme" {
		t.Errorf("expected the resource by URI, got %v", out.ConversationValues)
	}
}

// TestPlugin_MissingResourceIsReported makes sure a configured resource the
// server does not have does not fail the request.
func TestPlugin_MissingResourceIsReported(t *testing.T) {
	srv := newResourceMCPServer(t, nil)

	p := newTestPlugin(t)
	reqCtx := requestContext(srv.URL, `,"resources":["file:///gone.md"]`)

	out, err := p.ListTools(context.Background(), &proto.ListToolsInput{Ctx: reqCtx})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(out.ConversationValues) != 1 {
		t.Fatalf("expected the missing resource reported, got %v", out.ConversationValues)
	}
	if out.ConversationValues[0].GetContent() == "" {
		t.Error("expected the missing resource reported in the injected text")
	}
}

// TestPlugin_PrefetchToolDeclared checks the configured prefetch tool is
// declared to the host, with the arguments template kept for the host to fill.
func TestPlugin_PrefetchToolDeclared(t *testing.T) {
	srv := newResourceMCPServer(t, nil)

	p := newTestPlugin(t)
	reqCtx := requestContext(srv.URL, `,"prefetchTool":"echo","prefetchArguments":"{\"text\":\"{{last_user_message}}\"}","prefetchLabel":"Echo","prefetchResultTemplate":"[echo] {{result}}"`)

	out, err := p.ListTools(context.Background(), &proto.ListToolsInput{Ctx: reqCtx})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	if len(out.ConversationTools) != 1 {
		t.Fatalf("expected one conversation tool, got %v", out.ConversationTools)
	}
	declared := out.ConversationTools[0]
	if declared.GetTrigger() != proto.ToolConversation_ALWAYS {
		t.Errorf("expected the ALWAYS trigger, got %v", declared.GetTrigger())
	}
	if declared.GetTool().GetName() != "echo" {
		t.Errorf("expected the echo tool, got %q", declared.GetTool().GetName())
	}
	if declared.GetArgumentsJson() != `{"text":"{{last_user_message}}"}` {
		t.Errorf("expected the arguments template kept verbatim, got %q", declared.GetArgumentsJson())
	}
	if declared.GetLabel() != "Echo" || declared.GetResultTemplate() != "[echo] {{result}}" {
		t.Errorf("unexpected label/template: %+v", declared)
	}
}

// TestPlugin_PrefetchToolOutsideFilterDropped makes sure the tool filter is not
// bypassed by a prefetch: a tool the administrator did not allow is not
// prefetched either.
func TestPlugin_PrefetchToolOutsideFilterDropped(t *testing.T) {
	srv := newResourceMCPServer(t, nil)

	p := newTestPlugin(t)
	reqCtx := requestContext(srv.URL, `,"toolFilter":["other"],"prefetchTool":"echo"`)

	out, err := p.ListTools(context.Background(), &proto.ListToolsInput{Ctx: reqCtx})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(out.ConversationTools) != 0 {
		t.Errorf("expected no prefetch for a filtered-out tool, got %v", out.ConversationTools)
	}
}

// TestPlugin_OneSessionAcrossToolRounds checks the plugin resolves three tool
// rounds of one conversation on a single MCP session, which is what the
// connector is for.
func TestPlugin_OneSessionAcrossToolRounds(t *testing.T) {
	var sessions atomic.Int64
	seen := map[string]struct{}{}

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echoes text"}, echo)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.Header.Get("Mcp-Session-Id"); id != "" {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				sessions.Add(1)
			}
		}
		handler.ServeHTTP(w, r)
	}))
	// Cleanups run last registered first: the plugin closes the MCP sessions it
	// keeps open before the server waiting on them is torn down.
	t.Cleanup(srv.Close)

	p := newTestPlugin(t)
	reqCtx := requestContext(srv.URL, "")

	ctx := context.Background()
	if _, err := p.ListTools(ctx, &proto.ListToolsInput{Ctx: reqCtx}); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for round := range 3 {
		out, err := p.CallTool(ctx, &proto.CallToolInput{
			Ctx:           reqCtx,
			Name:          "echo",
			ArgumentsJson: `{"text":"round"}`,
		})
		if err != nil {
			t.Fatalf("round %d: CallTool: %v", round, err)
		}
		if out.GetResultText() != "echo: round" {
			t.Errorf("round %d: unexpected result %q", round, out.GetResultText())
		}
	}

	if got := sessions.Load(); got != 1 {
		t.Errorf("expected a single MCP session for the conversation, the server saw %d", got)
	}
}

// TestPlugin_SeparateConversationsSeparateSessions checks two conversations do
// not share a session: the state one leaves behind is not the other's.
func TestPlugin_SeparateConversationsSeparateSessions(t *testing.T) {
	var sessions atomic.Int64
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echoes text"}, echo)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Session-Id") == "" {
			sessions.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	p := newTestPlugin(t)

	first := requestContext(srv.URL, "")
	second := requestContext(srv.URL, "")
	second.SessionId = "session-2"

	for _, reqCtx := range []*proto.RequestContext{first, second} {
		if _, err := p.CallTool(context.Background(), &proto.CallToolInput{
			Ctx:           reqCtx,
			Name:          "echo",
			ArgumentsJson: `{"text":"hi"}`,
		}); err != nil {
			t.Fatalf("CallTool: %v", err)
		}
	}

	if got := sessions.Load(); got != 2 {
		t.Errorf("expected one session per conversation (2), saw %d", got)
	}
}
