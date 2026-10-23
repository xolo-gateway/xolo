package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

type fakeHostClient struct {
	mu      sync.Mutex
	secrets map[[4]string]string
}

func newFakeHostClient() *fakeHostClient {
	return &fakeHostClient{secrets: map[[4]string]string{}}
}

func (c *fakeHostClient) GetConfig(_ context.Context, _, _ string) (string, error) { return "{}", nil }
func (c *fakeHostClient) SaveConfig(_ context.Context, _, _, _ string) error       { return nil }
func (c *fakeHostClient) ListModels(_ context.Context, _ string) ([]*proto.ModelInfo, error) {
	return nil, nil
}

func (c *fakeHostClient) GetSecret(_ context.Context, scopeID, pluginName, nodeID, key string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.secrets[[4]string{scopeID, pluginName, nodeID, key}]
	return v, ok, nil
}

func (c *fakeHostClient) SetSecret(_ context.Context, scopeID, pluginName, nodeID, key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.secrets[[4]string{scopeID, pluginName, nodeID, key}] = value
	return nil
}

func (c *fakeHostClient) DeleteSecret(_ context.Context, scopeID, pluginName, nodeID, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.secrets, [4]string{scopeID, pluginName, nodeID, key})
	return nil
}

func (c *fakeHostClient) EmitEvent(_ context.Context, _ pluginsdk.Event) error { return nil }
func (c *fakeHostClient) ChatCompletion(_ context.Context, _ *proto.HostChatCompletionRequest) (*proto.HostChatCompletionResponse, error) {
	return nil, nil
}

var _ pluginsdk.HostClient = (*fakeHostClient)(nil)

type echoInput struct {
	Text string `json:"text" jsonschema:"text to echo"`
}

func echo(_ context.Context, _ *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + in.Text}}}, nil, nil
}

func newTestMCPServer(t *testing.T, wantAuth string) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echoes text"}, echo)

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if wantAuth != "" && r.Header.Get("Authorization") != wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		// The plugin under test keeps its MCP session open; the hanging
		// notification stream would make Close wait forever.
		srv.CloseClientConnections()
		srv.Close()
	})
	return srv
}

func TestPlugin_ListToolsAndCallTool_UsesStoredSecret(t *testing.T) {
	srv := newTestMCPServer(t, "Bearer s3cr3t")

	hc := newFakeHostClient()
	if err := hc.SetSecret(context.Background(), "org-1", "mcp-bridge", "node-1", secretKeyAuthValue, "Bearer s3cr3t"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	p := &Plugin{}
	t.Cleanup(p.Close)
	p.SetHostClient(hc)

	reqCtx := &proto.RequestContext{
		OrgId:         "org-1",
		SecretScopeId: "org-1",
		NodeId:        "node-1",
		ConfigJson:    `{"endpoint":"` + srv.URL + `/mcp","authHeaderName":"Authorization"}`,
	}

	listOut, err := p.ListTools(context.Background(), &proto.ListToolsInput{Ctx: reqCtx})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(listOut.Tools) != 1 || listOut.Tools[0].Name != "echo" {
		t.Fatalf("expected exactly one tool named 'echo', got %v", listOut.Tools)
	}

	callOut, err := p.CallTool(context.Background(), &proto.CallToolInput{
		Ctx:           reqCtx,
		Name:          "echo",
		ArgumentsJson: `{"text":"hi"}`,
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if callOut.IsError {
		t.Fatalf("unexpected error result: %s", callOut.ResultText)
	}
	if callOut.ResultText != "echo: hi" {
		t.Errorf("expected %q, got %q", "echo: hi", callOut.ResultText)
	}
}

func TestPlugin_ListTools_WrongSecret_Fails(t *testing.T) {
	srv := newTestMCPServer(t, "Bearer s3cr3t")

	hc := newFakeHostClient()
	if err := hc.SetSecret(context.Background(), "org-1", "mcp-bridge", "node-1", secretKeyAuthValue, "Bearer wrong"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	p := &Plugin{}
	t.Cleanup(p.Close)
	p.SetHostClient(hc)

	reqCtx := &proto.RequestContext{
		OrgId:         "org-1",
		SecretScopeId: "org-1",
		NodeId:        "node-1",
		ConfigJson:    `{"endpoint":"` + srv.URL + `/mcp","authHeaderName":"Authorization"}`,
	}

	if _, err := p.ListTools(context.Background(), &proto.ListToolsInput{Ctx: reqCtx}); err == nil {
		t.Fatal("expected ListTools to fail with the wrong auth secret")
	}
}
