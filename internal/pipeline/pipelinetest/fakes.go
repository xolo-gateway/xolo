// Package pipelinetest provides reusable fakes, builders and a test harness
// for exercising internal/pipeline.Engine in isolation (no HTTP/proxy stack,
// no GORM/SQLite stores).
package pipelinetest

import (
	"context"

	"github.com/bornholm/genai/llm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/pipeline"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"google.golang.org/grpc"
)

// LLMClient is a fake llm.Client returning a fixed response content.
type LLMClient struct {
	response string
}

// NewLLMClient creates an LLMClient that always answers with response.
func NewLLMClient(response string) *LLMClient {
	return &LLMClient{response: response}
}

func (c *LLMClient) ChatCompletion(_ context.Context, _ ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	return &chatResponse{content: c.response}, nil
}

func (c *LLMClient) ChatCompletionStream(_ context.Context, _ ...llm.ChatCompletionOptionFunc) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch, nil
}

func (c *LLMClient) Embeddings(_ context.Context, _ []string, _ ...llm.EmbeddingsOptionFunc) (llm.EmbeddingsResponse, error) {
	return nil, nil
}

func (c *LLMClient) Transcription(_ context.Context, _ []byte, _ ...llm.TranscriptionOptionFunc) (llm.TranscriptionResponse, error) {
	return nil, nil
}

var _ llm.Client = (*LLMClient)(nil)

type chatResponse struct{ content string }

func (r *chatResponse) Message() llm.Message           { return &chatMessage{r.content} }
func (r *chatResponse) ToolCalls() []llm.ToolCall      { return nil }
func (r *chatResponse) Usage() llm.ChatCompletionUsage { return nil }

type chatMessage struct{ content string }

func (m *chatMessage) Role() llm.Role                { return llm.RoleAssistant }
func (m *chatMessage) Content() string               { return m.content }
func (m *chatMessage) Attachments() []llm.Attachment { return nil }

// ModelResolver is a fake pipeline.ModelResolver. Unregistered proxy names
// fall back to an LLMClient answering "default".
type ModelResolver struct {
	clients map[string]llm.Client
}

// NewModelResolver creates an empty ModelResolver.
func NewModelResolver() *ModelResolver {
	return &ModelResolver{clients: make(map[string]llm.Client)}
}

// WithModel registers client as the resolution result for proxyName.
func (r *ModelResolver) WithModel(proxyName string, client llm.Client) *ModelResolver {
	r.clients[proxyName] = client
	return r
}

// WithResponse registers a fixed-response LLMClient for proxyName.
func (r *ModelResolver) WithResponse(proxyName, content string) *ModelResolver {
	return r.WithModel(proxyName, NewLLMClient(content))
}

func (r *ModelResolver) ResolveRealModel(_ context.Context, _ model.OrgID, proxyName string) (llm.Client, string, model.LLMModelID, error) {
	if c, ok := r.clients[proxyName]; ok {
		return c, proxyName, "", nil
	}
	return NewLLMClient("default"), proxyName, "", nil
}

var _ pipeline.ModelResolver = (*ModelResolver)(nil)

// VirtualModelStore is an in-memory port.VirtualModelStore. An empty store
// returns port.ErrNotFound for every lookup.
type VirtualModelStore struct {
	byID map[model.VirtualModelID]model.VirtualModel
}

// NewVirtualModelStore creates a VirtualModelStore pre-populated with vms.
func NewVirtualModelStore(vms ...model.VirtualModel) *VirtualModelStore {
	s := &VirtualModelStore{byID: make(map[model.VirtualModelID]model.VirtualModel)}
	for _, vm := range vms {
		s.Add(vm)
	}
	return s
}

// Add registers (or replaces) a VirtualModel in the store.
func (s *VirtualModelStore) Add(vm model.VirtualModel) {
	s.byID[vm.ID()] = vm
}

func (s *VirtualModelStore) CreateVirtualModel(_ context.Context, vm model.VirtualModel) error {
	s.Add(vm)
	return nil
}

func (s *VirtualModelStore) GetVirtualModelByID(_ context.Context, id model.VirtualModelID) (model.VirtualModel, error) {
	if vm, ok := s.byID[id]; ok {
		return vm, nil
	}
	return nil, port.ErrNotFound
}

func (s *VirtualModelStore) GetVirtualModelByName(_ context.Context, orgID model.OrgID, name string) (model.VirtualModel, error) {
	for _, vm := range s.byID {
		if vm.OrgID() == orgID && vm.Name() == name {
			return vm, nil
		}
	}
	return nil, port.ErrNotFound
}

func (s *VirtualModelStore) ListVirtualModels(_ context.Context, orgID model.OrgID) ([]model.VirtualModel, error) {
	var out []model.VirtualModel
	for _, vm := range s.byID {
		if vm.OrgID() == orgID {
			out = append(out, vm)
		}
	}
	return out, nil
}

func (s *VirtualModelStore) SaveVirtualModel(_ context.Context, vm model.VirtualModel) error {
	s.Add(vm)
	return nil
}

func (s *VirtualModelStore) DeleteVirtualModel(_ context.Context, id model.VirtualModelID) error {
	delete(s.byID, id)
	return nil
}

var _ port.VirtualModelStore = (*VirtualModelStore)(nil)

// PluginClient is a fake proto.XoloPluginClient driven by optional callbacks.
type PluginClient struct {
	PreRequestFunc        func(ctx context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error)
	PostResponseFunc      func(ctx context.Context, in *proto.PostResponseInput) (*proto.PostResponseOutput, error)
	ResolveModelFunc      func(ctx context.Context, in *proto.ResolveModelInput) (*proto.ResolveModelOutput, error)
	ListToolsFunc         func(ctx context.Context, in *proto.ListToolsInput) (*proto.ListToolsOutput, error)
	CallToolFunc          func(ctx context.Context, in *proto.CallToolInput) (*proto.CallToolOutput, error)
	InspectToolResultFunc func(ctx context.Context, in *proto.InspectToolResultInput) (*proto.InspectToolResultOutput, error)
}

func (c *PluginClient) Describe(_ context.Context, _ *proto.DescribeRequest, _ ...grpc.CallOption) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{}, nil
}

func (c *PluginClient) Initialize(_ context.Context, _ *proto.InitializeRequest, _ ...grpc.CallOption) (*proto.InitializeResponse, error) {
	return &proto.InitializeResponse{}, nil
}

func (c *PluginClient) PreRequest(ctx context.Context, in *proto.PreRequestInput, _ ...grpc.CallOption) (*proto.PreRequestOutput, error) {
	if c.PreRequestFunc != nil {
		return c.PreRequestFunc(ctx, in)
	}
	return &proto.PreRequestOutput{Allowed: true}, nil
}

func (c *PluginClient) PostResponse(ctx context.Context, in *proto.PostResponseInput, _ ...grpc.CallOption) (*proto.PostResponseOutput, error) {
	if c.PostResponseFunc != nil {
		return c.PostResponseFunc(ctx, in)
	}
	return &proto.PostResponseOutput{}, nil
}

func (c *PluginClient) ResolveModel(ctx context.Context, in *proto.ResolveModelInput, _ ...grpc.CallOption) (*proto.ResolveModelOutput, error) {
	if c.ResolveModelFunc != nil {
		return c.ResolveModelFunc(ctx, in)
	}
	return &proto.ResolveModelOutput{}, nil
}

func (c *PluginClient) ListModels(_ context.Context, _ *proto.ListModelsInput, _ ...grpc.CallOption) (*proto.ListModelsOutput, error) {
	return &proto.ListModelsOutput{}, nil
}

func (c *PluginClient) ListTools(ctx context.Context, in *proto.ListToolsInput, _ ...grpc.CallOption) (*proto.ListToolsOutput, error) {
	if c.ListToolsFunc != nil {
		return c.ListToolsFunc(ctx, in)
	}
	return &proto.ListToolsOutput{}, nil
}

func (c *PluginClient) CallTool(ctx context.Context, in *proto.CallToolInput, _ ...grpc.CallOption) (*proto.CallToolOutput, error) {
	if c.CallToolFunc != nil {
		return c.CallToolFunc(ctx, in)
	}
	return &proto.CallToolOutput{}, nil
}

func (c *PluginClient) InspectToolResult(ctx context.Context, in *proto.InspectToolResultInput, _ ...grpc.CallOption) (*proto.InspectToolResultOutput, error) {
	if c.InspectToolResultFunc != nil {
		return c.InspectToolResultFunc(ctx, in)
	}
	return &proto.InspectToolResultOutput{}, nil
}

var _ proto.XoloPluginClient = (*PluginClient)(nil)

// PluginProvider is a fake pipeline.PluginProvider backed by static maps.
type PluginProvider struct {
	clients map[string]proto.XoloPluginClient
	descs   map[string]*proto.PluginDescriptor
}

// NewPluginProvider creates an empty PluginProvider.
func NewPluginProvider() *PluginProvider {
	return &PluginProvider{
		clients: make(map[string]proto.XoloPluginClient),
		descs:   make(map[string]*proto.PluginDescriptor),
	}
}

// Register associates name with client and its descriptor.
func (p *PluginProvider) Register(name string, desc *proto.PluginDescriptor, client proto.XoloPluginClient) *PluginProvider {
	p.clients[name] = client
	p.descs[name] = desc
	return p
}

func (p *PluginProvider) GetOrRestart(_ context.Context, name string) (proto.XoloPluginClient, *proto.PluginDescriptor, bool) {
	c, ok := p.clients[name]
	if !ok {
		return nil, nil, false
	}
	return c, p.descs[name], true
}

var _ pipeline.PluginProvider = (*PluginProvider)(nil)
