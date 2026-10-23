package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/adapter/mcpclient"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

const secretKeyAuthValue = "authValue"

// idleTimeout is how long an MCP session of a conversation stays open without
// being used. A conversation outlives a request — an agent thinking for a
// minute between two tool calls, then asking again — so the session is kept,
// but not forever: a server that is not called for this long is worth
// reconnecting to when it is next asked for.
const idleTimeout = mcpclient.DefaultIdleTimeout

// Plugin implémente proto.XoloPluginServer.
type Plugin struct {
	proto.UnimplementedXoloPluginServer

	mu         sync.Mutex
	hostClient pluginsdk.HostClient
	oauth      *oauthClient
	// sessions holds one MCP session per server of a conversation, so the tool
	// rounds of a request share it instead of opening a session each time.
	sessions map[string]*mcpclient.Connector
}

// oauthClient returns the client shared by the tool calls and the UI.
func (p *Plugin) oauthClient() *oauthClient {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.oauth == nil {
		p.oauth = newOAuthClient()
	}
	return p.oauth
}

// SetHostClient implémente pluginsdk.HostClientSetter.
func (p *Plugin) SetHostClient(c pluginsdk.HostClient) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hostClient = c
}

func (p *Plugin) getHostClient() pluginsdk.HostClient {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hostClient
}

// connector returns the session holder of one conversation on one endpoint,
// creating it on first use.
//
// The key is the conversation (the proxy's session header, or the caller
// identity when the client sends none) plus the node and the endpoint: the same
// user talking on two pipelines, or with two MCP servers, gets two sessions,
// which is what the servers expect.
func (p *Plugin) connector(ec sessionScope, cfg Config) *mcpclient.Connector {
	key := strings.Join([]string{ec.key, ec.nodeID, cfg.Endpoint}, "\x00")

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessions == nil {
		p.sessions = map[string]*mcpclient.Connector{}
	}
	if c, ok := p.sessions[key]; ok {
		return c
	}
	connCfg := mcpclient.Config{
		Endpoint:       cfg.Endpoint,
		AuthHeaderName: ec.authHeaderName,
		AuthValue:      ec.authValue,
		Timeout:        time.Duration(cfg.TimeoutSeconds) * time.Second,
	}
	c := mcpclient.NewConnector(connCfg, idleTimeout)
	p.sessions[key] = c
	return c
}

// Close closes every pooled MCP session.
//
// Sessions are kept between requests on purpose, so nothing on the request path
// closes them: a plugin process shutting down has to, or the servers it talked to
// are left holding a stream open.
func (p *Plugin) Close() {
	p.mu.Lock()
	connectors := make([]*mcpclient.Connector, 0, len(p.sessions))
	for _, c := range p.sessions {
		connectors = append(connectors, c)
	}
	p.sessions = nil
	p.mu.Unlock()

	for _, c := range connectors {
		c.Close()
	}
}

// sessionScope is everything the key of a conversation session is made of,
// resolved from the request.
type sessionScope struct {
	key            string
	nodeID         string
	authHeaderName string
	authValue      string
}

// connect resolves the node's configuration and credentials, and returns the
// connector holding the conversation's session. The caller runs its work
// through its Do, which opens the session and replaces it when it died.
func (p *Plugin) connect(ctx context.Context, reqCtx *proto.RequestContext) (Config, *mcpclient.Connector, error) {
	cfg, err := parseConfig(reqCtx.GetConfigJson())
	if err != nil {
		return Config{}, nil, errors.Wrap(err, "parse config")
	}
	if cfg.Endpoint == "" {
		return Config{}, nil, errors.New("mcp-bridge: endpoint not configured")
	}

	authHeaderName := cfg.AuthHeaderName
	var authValue string
	if cfg.AuthMode == AuthModeOAuth {
		hc := p.getHostClient()
		if hc == nil {
			return cfg, nil, errors.WithStack(ErrNotAuthorized)
		}
		token, err := p.oauthClient().accessToken(ctx, hc, "mcp-bridge", reqCtx.GetNodeId(), reqCtx.GetUserId(), cfg.Endpoint)
		if err != nil {
			return cfg, nil, err
		}
		authHeaderName, authValue = "Authorization", "Bearer "+token
	} else if hc := p.getHostClient(); hc != nil {
		v, found, err := hc.GetSecret(ctx, reqCtx.GetSecretScopeId(), "mcp-bridge", reqCtx.GetNodeId(), secretKeyAuthValue)
		if err != nil {
			return Config{}, nil, errors.Wrap(err, "get auth secret")
		}
		if found {
			authValue = v
		}
	}

	connector := p.connector(sessionScope{
		key:            sessionKey(reqCtx),
		nodeID:         reqCtx.GetNodeId(),
		authHeaderName: authHeaderName,
		authValue:      authValue,
	}, cfg)

	return cfg, connector, nil
}

// sessionKey names the conversation a request belongs to. The host sets
// NodeId per node, and the proxy's session header per conversation; without
// one, the caller identity is the closest stable scope there is, which at
// least keeps a user's tool rounds on one session.
func sessionKey(reqCtx *proto.RequestContext) string {
	if reqCtx.GetSessionId() != "" {
		return reqCtx.GetSessionId()
	}
	return reqCtx.GetOrgId() + "/" + reqCtx.GetUserId()
}

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:         "mcp-bridge",
		Version:      "0.2.0",
		Description:  "Connecte un serveur MCP (Streamable HTTP) externe : expose ses tools au LLM, résout les appels d'outils côté serveur et injecte les tools et ressources configurés avant le premier appel au modèle.",
		Capabilities: []proto.PluginDescriptor_Capability{proto.PluginDescriptor_TOOL_PROVIDER},
		ConfigSchema: configSchemaJSON,
		InputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
		},
		OutputPorts: []*proto.PortDescriptor{
			{Name: "request", PortType: "request", Required: true},
		},
	}, nil
}

func (p *Plugin) ListTools(ctx context.Context, in *proto.ListToolsInput) (*proto.ListToolsOutput, error) {
	cfg, connector, err := p.connect(ctx, in.GetCtx())
	if errors.Is(err, ErrNotAuthorized) {
		// The user has to authorize the server first: the only tool offered
		// gives the link to do so.
		return &proto.ListToolsOutput{
			Tools: []*proto.ToolDescriptor{{
				Name:            connectToolName,
				Description:     "Donne le lien permettant à l'utilisateur d'autoriser l'accès au serveur MCP " + cfg.Endpoint + ". À appeler lorsque l'utilisateur demande une information que ce serveur pourrait fournir.",
				InputSchemaJson: `{"type":"object","properties":{}}`,
			}},
			MaxConsecutiveToolCalls: int32(cfg.MaxConsecutiveToolCalls),
		}, nil
	}
	if err != nil {
		return nil, err
	}

	var tools []*proto.ToolDescriptor
	var values []*proto.ConversationValue
	err = connector.Do(ctx, func(session *mcp.ClientSession) error {
		infos, err := mcpclient.ListToolInfos(ctx, session, cfg.ToolFilter)
		if err != nil {
			return err
		}
		tools = make([]*proto.ToolDescriptor, 0, len(infos))
		for _, info := range infos {
			tools = append(tools, &proto.ToolDescriptor{
				Name:            info.Name,
				Description:     info.Description,
				InputSchemaJson: info.InputSchemaJSON,
			})
		}
		values = readResources(ctx, session, cfg.Resources)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &proto.ListToolsOutput{
		Tools:                   tools,
		MaxConsecutiveToolCalls: int32(cfg.MaxConsecutiveToolCalls),
		ConversationTools:       conversationTools(cfg, tools),
		ConversationValues:      values,
	}, nil
}

// readResources reads the resources the node names in its configuration, on the
// session ListTools already opened.
//
// A resource that cannot be read is reported in the injected text rather than
// failing the request: a configuration naming a resource the server renamed
// should still let the conversation through, with a note the operator will see
// in the model's answer. Nothing is read from a server that does not advertise
// the resources capability — asking would only produce an error.
func readResources(ctx context.Context, session *mcp.ClientSession, refs []string) []*proto.ConversationValue {
	if len(refs) == 0 {
		return nil
	}

	values := make([]*proto.ConversationValue, 0, len(refs))
	for _, ref := range refs {
		info, err := mcpclient.FindResource(ctx, session, ref)
		if err != nil {
			values = append(values, &proto.ConversationValue{
				Label:   "MCP resource " + ref,
				Content: "the resource could not be read: " + err.Error(),
			})
			continue
		}
		text, err := mcpclient.ReadResourceText(ctx, session, info.URI)
		if err != nil {
			values = append(values, &proto.ConversationValue{
				Label:   "MCP resource " + ref,
				Content: "the resource could not be read: " + err.Error(),
			})
			continue
		}
		label := "MCP resource " + info.URI
		if info.Name != "" && info.Name != ref {
			label = "MCP resource " + info.Name + " (" + info.URI + ")"
		}
		values = append(values, &proto.ConversationValue{Label: label, Content: text})
	}
	return values
}

// conversationTools declares the tool the node asks the host to resolve before
// the model's first call, when the configuration names one. The declaration is
// dropped when the tool is not in the allowed list: the filter is what an
// administrator authorized, and a prefetch is no way around it.
func conversationTools(cfg Config, tools []*proto.ToolDescriptor) []*proto.ToolConversation {
	name := strings.TrimSpace(cfg.PrefetchTool)
	if name == "" {
		return nil
	}
	var declared *proto.ToolDescriptor
	for _, td := range tools {
		if td.GetName() == name {
			declared = td
			break
		}
	}
	if declared == nil {
		return nil
	}

	arguments := strings.TrimSpace(cfg.PrefetchArguments)
	if arguments == "" {
		arguments = "{}"
	}

	return []*proto.ToolConversation{{
		Trigger:        proto.ToolConversation_ALWAYS,
		Tool:           declared,
		ArgumentsJson:  arguments,
		ResultTemplate: cfg.PrefetchResultTemplate,
		Label:          cfg.PrefetchLabel,
	}}
}

func (p *Plugin) CallTool(ctx context.Context, in *proto.CallToolInput) (*proto.CallToolOutput, error) {
	cfg, connector, err := p.connect(ctx, in.GetCtx())
	if errors.Is(err, ErrNotAuthorized) {
		return &proto.CallToolOutput{
			ResultText: "L'utilisateur doit d'abord autoriser l'accès au serveur MCP en ouvrant ce lien, puis renouveler sa demande : " + connectURL(cfg, in.GetCtx().GetNodeId()),
			IsError:    in.Name != connectToolName,
		}, nil
	}
	if err != nil {
		return nil, err
	}

	var text string
	var isError bool
	err = connector.Do(ctx, func(session *mcp.ClientSession) error {
		var callErr error
		text, isError, callErr = mcpclient.CallToolText(ctx, session, in.Name, in.ArgumentsJson)
		return callErr
	})
	if err != nil {
		return nil, err
	}
	return &proto.CallToolOutput{ResultText: text, IsError: isError}, nil
}
