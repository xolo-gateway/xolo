package pluginsdk

import (
	"context"
	"fmt"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"google.golang.org/grpc"
)

// HostClient is the interface plugins use to interact with Xolo's XoloHostService gRPC.
type HostClient interface {
	GetConfig(ctx context.Context, orgID, pluginName string) (string, error)
	SaveConfig(ctx context.Context, orgID, pluginName, configJSON string) error
	// ListModels returns all enabled LLM models available for the given org.
	ListModels(ctx context.Context, orgID string) ([]*proto.ModelInfo, error)
	// GetSecret returns the value stored for (orgID, pluginName, nodeID, key),
	// and whether it was found.
	GetSecret(ctx context.Context, orgID, pluginName, nodeID, key string) (string, bool, error)
	// SetSecret persists value for (orgID, pluginName, nodeID, key).
	SetSecret(ctx context.Context, orgID, pluginName, nodeID, key, value string) error
	// DeleteSecret removes the value stored for (orgID, pluginName, nodeID, key).
	DeleteSecret(ctx context.Context, orgID, pluginName, nodeID, key string) error
	// EmitEvent records an event in Xolo's event system. The host forces the
	// event source to the plugin name and namespaces the type under
	// "plugin.<name>.". orgID/userID may be empty for platform-global events.
	EmitEvent(ctx context.Context, event Event) error
	// ChatCompletion calls a model of the org through the gateway (real or
	// virtual, resolved like any request) and returns the answer.
	ChatCompletion(ctx context.Context, req *proto.HostChatCompletionRequest) (*proto.HostChatCompletionResponse, error)
}

// Event is a plugin-emitted event. Severity should be one of "info", "warning"
// or "error" (defaults to "info").
type Event struct {
	PluginName string
	OrgID      string
	UserID     string
	Type       string
	Severity   string
	Message    string
	Attributes map[string]string
}

// grpcHostClient implements HostClient over gRPC.
type grpcHostClient struct {
	client proto.XoloHostServiceClient
}

func newGRPCHostClient(conn *grpc.ClientConn) *grpcHostClient {
	return &grpcHostClient{client: proto.NewXoloHostServiceClient(conn)}
}

func (c *grpcHostClient) GetConfig(ctx context.Context, orgID, pluginName string) (string, error) {
	resp, err := c.client.GetConfig(ctx, &proto.GetConfigRequest{
		OrgId:      orgID,
		PluginName: pluginName,
	})
	if err != nil {
		return "", fmt.Errorf("GetConfig gRPC: %w", err)
	}
	if resp.ConfigJson == "" {
		return "{}", nil
	}
	return resp.ConfigJson, nil
}

func (c *grpcHostClient) SaveConfig(ctx context.Context, orgID, pluginName, configJSON string) error {
	_, err := c.client.SaveConfig(ctx, &proto.SaveConfigRequest{
		OrgId:      orgID,
		PluginName: pluginName,
		ConfigJson: configJSON,
	})
	if err != nil {
		return fmt.Errorf("SaveConfig gRPC: %w", err)
	}
	return nil
}

func (c *grpcHostClient) ListModels(ctx context.Context, orgID string) ([]*proto.ModelInfo, error) {
	resp, err := c.client.ListModels(ctx, &proto.ListModelsForOrgRequest{OrgId: orgID})
	if err != nil {
		return nil, fmt.Errorf("ListModels gRPC: %w", err)
	}
	return resp.Models, nil
}

func (c *grpcHostClient) GetSecret(ctx context.Context, orgID, pluginName, nodeID, key string) (string, bool, error) {
	resp, err := c.client.GetSecret(ctx, &proto.GetSecretRequest{
		OrgId:      orgID,
		PluginName: pluginName,
		NodeId:     nodeID,
		Key:        key,
	})
	if err != nil {
		return "", false, fmt.Errorf("GetSecret gRPC: %w", err)
	}
	return resp.Value, resp.Found, nil
}

func (c *grpcHostClient) SetSecret(ctx context.Context, orgID, pluginName, nodeID, key, value string) error {
	_, err := c.client.SetSecret(ctx, &proto.SetSecretRequest{
		OrgId:      orgID,
		PluginName: pluginName,
		NodeId:     nodeID,
		Key:        key,
		Value:      value,
	})
	if err != nil {
		return fmt.Errorf("SetSecret gRPC: %w", err)
	}
	return nil
}

func (c *grpcHostClient) EmitEvent(ctx context.Context, event Event) error {
	_, err := c.client.EmitEvent(ctx, &proto.EmitEventRequest{
		PluginName: event.PluginName,
		OrgId:      event.OrgID,
		UserId:     event.UserID,
		Type:       event.Type,
		Severity:   event.Severity,
		Message:    event.Message,
		Attributes: event.Attributes,
	})
	if err != nil {
		return fmt.Errorf("EmitEvent gRPC: %w", err)
	}
	return nil
}

func (c *grpcHostClient) DeleteSecret(ctx context.Context, orgID, pluginName, nodeID, key string) error {
	_, err := c.client.DeleteSecret(ctx, &proto.DeleteSecretRequest{
		OrgId:      orgID,
		PluginName: pluginName,
		NodeId:     nodeID,
		Key:        key,
	})
	if err != nil {
		return fmt.Errorf("DeleteSecret gRPC: %w", err)
	}
	return nil
}

func (c *grpcHostClient) ChatCompletion(ctx context.Context, req *proto.HostChatCompletionRequest) (*proto.HostChatCompletionResponse, error) {
	resp, err := c.client.ChatCompletion(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ChatCompletion gRPC: %w", err)
	}
	return resp, nil
}
