package main

import (
	"context"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/adapter/mcpclient"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

const secretKeyAuthValue = "authValue"

// Plugin implémente proto.XoloPluginServer.
type Plugin struct {
	proto.UnimplementedXoloPluginServer

	mu         sync.Mutex
	hostClient pluginsdk.HostClient
	oauth      *oauthClient
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

func (p *Plugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{
		Name:         "mcp-bridge",
		Version:      "0.0.1",
		Description:  "Connecte un serveur MCP (Streamable HTTP) externe : expose ses tools au LLM et résout les appels d'outils côté serveur.",
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

// connect parses the node's config and opens an MCP session, resolving the
// auth value from the per-node secret store (never from ConfigJson).
func (p *Plugin) connect(ctx context.Context, reqCtx *proto.RequestContext) (*mcp.ClientSession, Config, error) {
	cfg, err := parseConfig(reqCtx.GetConfigJson())
	if err != nil {
		return nil, Config{}, errors.Wrap(err, "parse config")
	}
	if cfg.Endpoint == "" {
		return nil, Config{}, errors.New("mcp-bridge: endpoint not configured")
	}

	authHeaderName := cfg.AuthHeaderName
	var authValue string
	if cfg.AuthMode == AuthModeOAuth {
		hc := p.getHostClient()
		if hc == nil {
			return nil, cfg, errors.WithStack(ErrNotAuthorized)
		}
		token, err := p.oauthClient().accessToken(ctx, hc, "mcp-bridge", reqCtx.GetOrgId(), reqCtx.GetNodeId(), reqCtx.GetUserId(), cfg.Endpoint)
		if err != nil {
			return nil, cfg, err
		}
		authHeaderName, authValue = "Authorization", "Bearer "+token
	} else if hc := p.getHostClient(); hc != nil {
		v, found, err := hc.GetSecret(ctx, reqCtx.GetOrgId(), "mcp-bridge", reqCtx.GetNodeId(), secretKeyAuthValue)
		if err != nil {
			return nil, Config{}, errors.Wrap(err, "get auth secret")
		}
		if found {
			authValue = v
		}
	}

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	session, err := mcpclient.Connect(ctx, mcpclient.Config{
		Endpoint:       cfg.Endpoint,
		AuthHeaderName: authHeaderName,
		AuthValue:      authValue,
		Timeout:        timeout,
	})
	if err != nil {
		return nil, Config{}, errors.Wrap(err, "connect to MCP server")
	}
	return session, cfg, nil
}

func (p *Plugin) ListTools(ctx context.Context, in *proto.ListToolsInput) (*proto.ListToolsOutput, error) {
	session, cfg, err := p.connect(ctx, in.GetCtx())
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
	defer session.Close()

	infos, err := mcpclient.ListToolInfos(ctx, session, cfg.ToolFilter)
	if err != nil {
		return nil, err
	}

	tools := make([]*proto.ToolDescriptor, 0, len(infos))
	for _, info := range infos {
		tools = append(tools, &proto.ToolDescriptor{
			Name:            info.Name,
			Description:     info.Description,
			InputSchemaJson: info.InputSchemaJSON,
		})
	}
	return &proto.ListToolsOutput{
		Tools:                   tools,
		MaxConsecutiveToolCalls: int32(cfg.MaxConsecutiveToolCalls),
	}, nil
}

func (p *Plugin) CallTool(ctx context.Context, in *proto.CallToolInput) (*proto.CallToolOutput, error) {
	session, cfg, err := p.connect(ctx, in.GetCtx())
	if errors.Is(err, ErrNotAuthorized) {
		return &proto.CallToolOutput{
			ResultText: "L'utilisateur doit d'abord autoriser l'accès au serveur MCP en ouvrant ce lien, puis renouveler sa demande : " + connectURL(cfg, in.GetCtx().GetNodeId()),
			IsError:    in.Name != connectToolName,
		}, nil
	}
	if err != nil {
		return nil, err
	}
	defer session.Close()

	text, isError, err := mcpclient.CallToolText(ctx, session, in.Name, in.ArgumentsJson)
	if err != nil {
		return nil, err
	}
	return &proto.CallToolOutput{ResultText: text, IsError: isError}, nil
}
