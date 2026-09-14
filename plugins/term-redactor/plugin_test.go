package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

type fakePluginHostClient struct {
	secrets map[string]string
}

func newFakePluginHostClient() *fakePluginHostClient {
	return &fakePluginHostClient{secrets: map[string]string{}}
}

func (c *fakePluginHostClient) GetConfig(_ context.Context, _, _ string) (string, error) {
	return "{}", nil
}
func (c *fakePluginHostClient) SaveConfig(_ context.Context, _, _, _ string) error { return nil }
func (c *fakePluginHostClient) ListModels(_ context.Context, _ string) ([]*proto.ModelInfo, error) {
	return nil, nil
}
func (c *fakePluginHostClient) GetSecret(_ context.Context, _, _, nodeID, key string) (string, bool, error) {
	v, ok := c.secrets[nodeID+":"+key]
	return v, ok, nil
}
func (c *fakePluginHostClient) SetSecret(_ context.Context, _, _, nodeID, key, value string) error {
	c.secrets[nodeID+":"+key] = value
	return nil
}
func (c *fakePluginHostClient) DeleteSecret(_ context.Context, _, _, nodeID, key string) error {
	delete(c.secrets, nodeID+":"+key)
	return nil
}
func (c *fakePluginHostClient) EmitEvent(_ context.Context, _ pluginsdk.Event) error { return nil }
func (c *fakePluginHostClient) ChatCompletion(_ context.Context, _ *proto.HostChatCompletionRequest) (*proto.HostChatCompletionResponse, error) {
	return nil, nil
}

var _ pluginsdk.HostClient = (*fakePluginHostClient)(nil)

func TestPlugin_PreRequest_RedactsListedName_RoundTripsThroughPostResponse(t *testing.T) {
	fetcher := &fakeFetcher{terms: []termEntry{{UUID: "u1", Name: "Jean Dupont", Category: "client"}}}
	p := &Plugin{cache: newTermCache(fetcher)}
	p.SetHostClient(newFakePluginHostClient())

	reqCtx := &proto.RequestContext{OrgId: "org-1", NodeId: "node-1", ConfigJson: "{}"}
	in := &proto.PreRequestInput{
		Ctx:          reqCtx,
		MessagesJson: `[{"role":"user","content":"Contactez Jean Dupont stp"}]`,
	}

	out, err := p.PreRequest(context.Background(), in)
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	if !out.Allowed {
		t.Fatalf("expected the request to be allowed, got rejection: %s", out.RejectionReason)
	}

	wantToken := tokenForUUID("u1", "client", defaultTokenPrefixFallback, defaultTokenHexLength)

	var messages []map[string]any
	if err := json.Unmarshal([]byte(out.ModifiedMessagesJson), &messages); err != nil {
		t.Fatalf("unmarshal ModifiedMessagesJson: %v", err)
	}
	content, _ := messages[0]["content"].(string)
	if strings.Contains(content, "Jean Dupont") {
		t.Errorf("content = %q, must not leak the raw name", content)
	}
	if !strings.Contains(content, wantToken) {
		t.Errorf("content = %q, want it to contain the deterministic token %q", content, wantToken)
	}
	if len(out.NodeState) == 0 {
		t.Fatal("expected non-empty NodeState to carry the mapping to PostResponse")
	}

	postOut, err := p.PostResponse(context.Background(), &proto.PostResponseInput{
		NodeState:       out.NodeState,
		ResponseContent: "Bien reçu, " + wantToken + " sera recontacté.",
	})
	if err != nil {
		t.Fatalf("PostResponse: %v", err)
	}
	if !strings.Contains(postOut.ModifiedResponseContent, "Jean Dupont") {
		t.Errorf("ModifiedResponseContent = %q, want the original name restored", postOut.ModifiedResponseContent)
	}
	if strings.Contains(postOut.ModifiedResponseContent, wantToken) {
		t.Errorf("ModifiedResponseContent = %q, still contains the token", postOut.ModifiedResponseContent)
	}
}

func TestPlugin_PreRequest_UnlistedNameLeftUntouched(t *testing.T) {
	fetcher := &fakeFetcher{terms: []termEntry{{UUID: "u1", Name: "Jean Dupont", Category: "client"}}}
	p := &Plugin{cache: newTermCache(fetcher)}
	p.SetHostClient(newFakePluginHostClient())

	reqCtx := &proto.RequestContext{OrgId: "org-1", NodeId: "node-1", ConfigJson: "{}"}
	in := &proto.PreRequestInput{
		Ctx:          reqCtx,
		MessagesJson: `[{"role":"user","content":"Bonjour, comment allez-vous ?"}]`,
	}

	out, err := p.PreRequest(context.Background(), in)
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	if !out.NoResponseRewrite {
		t.Error("expected NoResponseRewrite when nothing was redacted")
	}

	var messages []map[string]any
	if err := json.Unmarshal([]byte(out.ModifiedMessagesJson), &messages); err != nil {
		t.Fatalf("unmarshal ModifiedMessagesJson: %v", err)
	}
	content, _ := messages[0]["content"].(string)
	if content != "Bonjour, comment allez-vous ?" {
		t.Errorf("content = %q, want it unchanged", content)
	}
}

func TestPlugin_PreRequest_ColdCacheFetchFailure_PassesThrough(t *testing.T) {
	fetcher := &fakeFetcher{err: errors.New("api unreachable")}
	p := &Plugin{cache: newTermCache(fetcher)}
	p.SetHostClient(newFakePluginHostClient())

	reqCtx := &proto.RequestContext{OrgId: "org-1", NodeId: "node-1", ConfigJson: "{}"}
	in := &proto.PreRequestInput{
		Ctx:          reqCtx,
		MessagesJson: `[{"role":"user","content":"Contactez Jean Dupont stp"}]`,
	}

	out, err := p.PreRequest(context.Background(), in)
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	if !out.Allowed {
		t.Fatalf("expected fail-open (allowed, unmodified), got rejection: %s", out.RejectionReason)
	}
	if out.ModifiedMessagesJson != "" {
		t.Errorf("ModifiedMessagesJson = %q, want empty (untouched passthrough)", out.ModifiedMessagesJson)
	}
	if !out.NoResponseRewrite {
		t.Error("expected NoResponseRewrite on a passthrough")
	}
}

func TestPlugin_PreRequest_InvalidConfig_PassesThrough(t *testing.T) {
	p := &Plugin{cache: newTermCache(&fakeFetcher{})}
	p.SetHostClient(newFakePluginHostClient())

	in := &proto.PreRequestInput{
		Ctx:          &proto.RequestContext{OrgId: "org-1", NodeId: "node-1", ConfigJson: "not json"},
		MessagesJson: `[{"role":"user","content":"peu importe"}]`,
	}

	out, err := p.PreRequest(context.Background(), in)
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	if !out.Allowed || out.ModifiedMessagesJson != "" {
		t.Errorf("expected an unmodified passthrough on invalid config, got %+v", out)
	}
}

func TestPlugin_PostResponse_NoNodeState_ReturnsUnmodified(t *testing.T) {
	p := &Plugin{}
	out, err := p.PostResponse(context.Background(), &proto.PostResponseInput{ResponseContent: "hello"})
	if err != nil {
		t.Fatalf("PostResponse: %v", err)
	}
	if out.ModifiedResponseContent != "" {
		t.Errorf("ModifiedResponseContent = %q, want empty when there is no state to restore", out.ModifiedResponseContent)
	}
}
