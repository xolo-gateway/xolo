package plugin

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/crypto"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// XoloHostService implements proto.XoloHostServiceServer.
// Config persistence uses an in-memory store keyed by (orgID, pluginName).
// This config is used by the plugin HTTP UI for display/edit; the authoritative
// per-node config lives in the pipeline graph (PluginNodeData.Config).
//
// GetSecret/SetSecret/DeleteSecret are a separate, persisted channel scoped
// per node instance (orgID, pluginName, nodeID, key), used by plugins to
// store sensitive values (e.g. an MCP server auth token) that must never
// appear in the pipeline graph's visible JSON.
type XoloHostService struct {
	proto.UnimplementedXoloHostServiceServer
	completer         ModelCompleter
	providerStore     port.ProviderStore
	virtualModelStore port.VirtualModelStore
	secretStore       port.SecretStore
	eventEmitter      port.EventEmitter
	secretKey         string
	mu                sync.RWMutex
	configs           map[string]string // key: orgID+":"+pluginName → JSON
}

// NewXoloHostService creates an XoloHostService.
func NewXoloHostService(
	providerStore port.ProviderStore,
	virtualModelStore port.VirtualModelStore,
	secretStore port.SecretStore,
	eventEmitter port.EventEmitter,
	secretKey string,
) *XoloHostService {
	return &XoloHostService{
		providerStore:     providerStore,
		virtualModelStore: virtualModelStore,
		secretStore:       secretStore,
		eventEmitter:      eventEmitter,
		secretKey:         secretKey,
		configs:           make(map[string]string),
	}
}

// ModelCompleter runs a chat completion against a model of an org on behalf
// of a plugin. It is implemented by the pipeline hook adapter, which resolves
// virtual models the same way a proxied request would.
type ModelCompleter interface {
	Complete(ctx context.Context, req ModelCompletionRequest) (*ModelCompletionResult, error)
}

// ModelCompletionRequest is the host-side form of HostChatCompletionRequest.
type ModelCompletionRequest struct {
	OrgID        model.OrgID
	UserID       model.UserID
	ProxyName    string
	Messages     []ChatMessage
	Temperature  *float64
	MaxTokens    *int
	JSONResponse bool
}

// ChatMessage is a plain role/content pair.
type ChatMessage struct {
	Role    string
	Content string
}

// ModelCompletionResult is what the model answered.
type ModelCompletionResult struct {
	Content          string
	PromptTokens     int64
	CompletionTokens int64
	ResolvedModel    string
}

// SetModelCompleter enables the ChatCompletion RPC. Without it the RPC answers
// Unavailable, which plugins must treat as "no model access on this host".
func (s *XoloHostService) SetModelCompleter(c ModelCompleter) { s.completer = c }

// ChatCompletion calls a model of the org for a plugin.
func (s *XoloHostService) ChatCompletion(ctx context.Context, req *proto.HostChatCompletionRequest) (*proto.HostChatCompletionResponse, error) {
	if s.completer == nil {
		return nil, status.Error(codes.Unavailable, "chat completion is not available on this host")
	}
	if req.GetOrgId() == "" || req.GetModel() == "" {
		return nil, status.Error(codes.InvalidArgument, "org_id and model are required")
	}
	if len(req.GetMessages()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one message is required")
	}

	mreq := ModelCompletionRequest{
		OrgID:        model.OrgID(req.OrgId),
		UserID:       model.UserID(req.UserId),
		ProxyName:    req.Model,
		JSONResponse: req.JsonResponse,
	}
	for _, m := range req.Messages {
		mreq.Messages = append(mreq.Messages, ChatMessage{Role: m.Role, Content: m.Content})
	}
	if req.Temperature > 0 {
		t := req.Temperature
		mreq.Temperature = &t
	}
	if req.MaxTokens > 0 {
		n := int(req.MaxTokens)
		mreq.MaxTokens = &n
	}

	res, err := s.completer.Complete(ctx, mreq)
	if err != nil {
		slog.WarnContext(ctx, "host service: chat completion failed", slog.String("model", req.Model), slog.Any("error", err))
		return nil, status.Errorf(codes.Internal, "chat completion: %v", err)
	}
	return &proto.HostChatCompletionResponse{
		Content:          res.Content,
		PromptTokens:     res.PromptTokens,
		CompletionTokens: res.CompletionTokens,
		ResolvedModel:    res.ResolvedModel,
	}, nil
}

func (s *XoloHostService) configKey(orgID, pluginName string) string {
	return orgID + ":" + pluginName
}

// GetConfig returns the in-memory config for the plugin (defaults to "{}").
func (s *XoloHostService) GetConfig(_ context.Context, req *proto.GetConfigRequest) (*proto.GetConfigResponse, error) {
	s.mu.RLock()
	cfg, ok := s.configs[s.configKey(req.OrgId, req.PluginName)]
	s.mu.RUnlock()
	if !ok || cfg == "" {
		cfg = "{}"
	}
	return &proto.GetConfigResponse{ConfigJson: cfg}, nil
}

// SaveConfig persists the plugin config in memory so the UI can read it back.
func (s *XoloHostService) SaveConfig(_ context.Context, req *proto.SaveConfigRequest) (*proto.SaveConfigResponse, error) {
	s.mu.Lock()
	s.configs[s.configKey(req.OrgId, req.PluginName)] = req.ConfigJson
	s.mu.Unlock()
	slog.Debug("host service: config saved", slog.String("plugin", req.PluginName))
	return &proto.SaveConfigResponse{}, nil
}

// SeedConfig pre-populates the in-memory config (called before opening the plugin UI).
func (s *XoloHostService) SeedConfig(orgID, pluginName, configJSON string) {
	s.mu.Lock()
	s.configs[s.configKey(orgID, pluginName)] = configJSON
	s.mu.Unlock()
}

// ReadConfig retrieves the in-memory config (called after the plugin UI saves).
func (s *XoloHostService) ReadConfig(orgID, pluginName string) string {
	s.mu.RLock()
	cfg := s.configs[s.configKey(orgID, pluginName)]
	s.mu.RUnlock()
	if cfg == "" {
		return "{}"
	}
	return cfg
}

func (s *XoloHostService) ListModels(ctx context.Context, req *proto.ListModelsForOrgRequest) (*proto.ListModelsForOrgResponse, error) {
	if s.providerStore == nil {
		return &proto.ListModelsForOrgResponse{}, nil
	}
	orgID := model.OrgID(req.OrgId)
	models, err := s.providerStore.ListEnabledLLMModels(ctx, orgID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list models: %v", err)
	}
	protoModels := make([]*proto.ModelInfo, 0, len(models))
	for _, m := range models {
		caps := m.Capabilities()
		protoModels = append(protoModels, &proto.ModelInfo{
			ProxyName:                  m.ProxyName(),
			RealModel:                  m.RealModel(),
			ProviderId:                 string(m.ProviderID()),
			PromptCostPer_1KTokens:     float64(m.PromptCostPer1KTokens()),
			CompletionCostPer_1KTokens: float64(m.CompletionCostPer1KTokens()),
			TokenLimit:                 m.ContextWindow(),
			IsVirtual:                  m.IsVirtual(),
			ContextLength:              m.ContextWindow(),
			SupportsVision:             caps.Vision,
			SupportsReasoning:          caps.Reasoning,
			SupportsEmbeddings:         caps.Embeddings,
			ActiveParamsBillions:       float32(m.ActiveParams()) / 1e9,
		})
	}
	if s.virtualModelStore != nil {
		virtualModels, err := s.virtualModelStore.ListVirtualModels(ctx, orgID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list virtual models: %v", err)
		}
		for _, vm := range virtualModels {
			protoModels = append(protoModels, &proto.ModelInfo{
				ProxyName: vm.Name(),
				IsVirtual: true,
			})
		}
	}
	return &proto.ListModelsForOrgResponse{Models: protoModels}, nil
}

// GetSecret returns the decrypted secret value for (orgID, pluginName, nodeID, key).
func (s *XoloHostService) GetSecret(ctx context.Context, req *proto.GetSecretRequest) (*proto.GetSecretResponse, error) {
	if s.secretStore == nil {
		return &proto.GetSecretResponse{Found: false}, nil
	}
	encrypted, found, err := s.secretStore.GetSecret(ctx, req.OrgId, req.PluginName, req.NodeId, req.Key)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get secret: %v", err)
	}
	if !found {
		return &proto.GetSecretResponse{Found: false}, nil
	}
	value, err := crypto.Decrypt(s.secretKey, encrypted)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decrypt secret: %v", err)
	}
	return &proto.GetSecretResponse{Value: value, Found: true}, nil
}

// SetSecret encrypts and persists value for (orgID, pluginName, nodeID, key).
func (s *XoloHostService) SetSecret(ctx context.Context, req *proto.SetSecretRequest) (*proto.SetSecretResponse, error) {
	if s.secretStore == nil {
		return nil, status.Error(codes.Unavailable, "secret store not configured")
	}
	encrypted, err := crypto.Encrypt(s.secretKey, req.Value)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt secret: %v", err)
	}
	if err := s.secretStore.SetSecret(ctx, req.OrgId, req.PluginName, req.NodeId, req.Key, encrypted); err != nil {
		return nil, status.Errorf(codes.Internal, "set secret: %v", err)
	}
	return &proto.SetSecretResponse{}, nil
}

// EmitEvent records an event emitted by a plugin. The source is forced to the
// plugin name and the type is namespaced under "plugin.<name>." so a plugin can
// never impersonate a platform event.
func (s *XoloHostService) EmitEvent(ctx context.Context, req *proto.EmitEventRequest) (*proto.EmitEventResponse, error) {
	if s.eventEmitter == nil {
		return &proto.EmitEventResponse{}, nil
	}

	pluginName := req.PluginName
	if pluginName == "" {
		pluginName = "plugin"
	}

	eventType := req.Type
	prefix := "plugin." + pluginName + "."
	if !strings.HasPrefix(eventType, prefix) {
		eventType = prefix + eventType
	}

	severity := model.EventSeverity(req.Severity)
	switch severity {
	case model.SeverityInfo, model.SeverityWarning, model.SeverityError:
	default:
		severity = model.SeverityInfo
	}

	opts := []model.EventOption{
		model.WithEventOrg(model.OrgID(req.OrgId)),
		model.WithEventSeverity(severity),
		model.WithEventMessage(req.Message),
		model.WithEventAttributes(req.Attributes),
	}
	if req.UserId != "" {
		opts = append(opts, model.WithEventUser(model.UserID(req.UserId)))
	}

	s.eventEmitter.Emit(ctx, model.NewEvent(pluginName, eventType, opts...))
	return &proto.EmitEventResponse{}, nil
}

// DeleteSecret removes the secret for (orgID, pluginName, nodeID, key).
func (s *XoloHostService) DeleteSecret(ctx context.Context, req *proto.DeleteSecretRequest) (*proto.DeleteSecretResponse, error) {
	if s.secretStore == nil {
		return &proto.DeleteSecretResponse{}, nil
	}
	if err := s.secretStore.DeleteSecret(ctx, req.OrgId, req.PluginName, req.NodeId, req.Key); err != nil {
		return nil, status.Errorf(codes.Internal, "delete secret: %v", err)
	}
	return &proto.DeleteSecretResponse{}, nil
}
