package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/crypto"
)

// pathRecorder is an upstream that refuses every request with a 400, which
// no SDK retries, and keeps the paths it was asked for.
type pathRecorder struct {
	mu     sync.Mutex
	paths  []string
	bodies []map[string]any
}

func (r *pathRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	r.mu.Lock()
	r.paths = append(r.paths, req.URL.Path)
	r.bodies = append(r.bodies, body)
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"recorded"}}`))
}

func (r *pathRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

// useProviderAt points the world's openai provider at baseURL. The fake store
// serves w.provider whatever the ID, so the models keep resolving to it.
func useProviderAt(t *testing.T, w *teamsWorld, baseURL string) {
	t.Helper()
	key, err := crypto.Encrypt(teamsTestSecretKey, "sk-test")
	require.NoError(t, err)
	w.provider = model.NewProvider(w.org.ID(), "openai", "openai", baseURL, key, "USD")
}

// callModel resolves name through the router and sends one chat completion,
// whose upstream error is expected and ignored.
func callModel(t *testing.T, w *teamsWorld, router *OrgModelRouter, name string) {
	t.Helper()
	ctx := w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, nil)
	client, _, err := router.ResolveModel(ctx, w.request("acme/"+name))
	require.NoError(t, err)
	_, _ = client.ChatCompletion(ctx, llm.WithMessages(llm.NewMessage(llm.RoleUser, "hi")))
}

func TestOrgModelRouterServesAModelThroughItsOwnAPIType(t *testing.T) {
	upstream := &pathRecorder{}
	srv := httptest.NewServer(upstream)
	defer srv.Close()

	w := newTeamsWorld(t)
	useProviderAt(t, w, srv.URL+"/v1")
	claude := model.NewLLMModel(w.provider.ID(), w.org.ID(), "claude", "claude-real", "", 1000, 1000)
	claude.SetAPIType("anthropic")
	claude.SetOutputWindow(8192)
	w.models["claude"] = claude
	router := NewOrgModelRouter(teamsProviderStore{w: w}, teamsOrgStore{w: w}, teamsTestSecretKey)

	callModel(t, w, router, "gpt")
	callModel(t, w, router, "claude")

	require.Equal(t, []string{"/v1/chat/completions", "/v1/messages"}, upstream.recorded(),
		"the openai provider serves gpt through chat completions and claude through Anthropic Messages")
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	require.EqualValues(t, 8192, upstream.bodies[1]["max_tokens"],
		"an anthropic model under an openai provider gets the anthropic max_tokens default")
}

func TestOrgModelRouterReachesAModelAtItsOwnBaseURL(t *testing.T) {
	providerUpstream := &pathRecorder{}
	providerSrv := httptest.NewServer(providerUpstream)
	defer providerSrv.Close()
	modelUpstream := &pathRecorder{}
	modelSrv := httptest.NewServer(modelUpstream)
	defer modelSrv.Close()

	w := newTeamsWorld(t)
	useProviderAt(t, w, providerSrv.URL+"/v1")
	elsewhere := model.NewLLMModel(w.provider.ID(), w.org.ID(), "elsewhere", "elsewhere-real", "", 1000, 1000)
	elsewhere.SetBaseURL(modelSrv.URL + "/v1")
	w.models["elsewhere"] = elsewhere
	router := NewOrgModelRouter(teamsProviderStore{w: w}, teamsOrgStore{w: w}, teamsTestSecretKey)

	callModel(t, w, router, "elsewhere")
	callModel(t, w, router, "gpt")

	require.Equal(t, []string{"/v1/chat/completions"}, modelUpstream.recorded(), "the model goes to its own URL")
	require.Equal(t, []string{"/v1/chat/completions"}, providerUpstream.recorded(), "the other models keep the provider's")
}

func TestOrgModelRouterRefusesEmbeddingsOnAnAPITypeWithoutThem(t *testing.T) {
	w := newTeamsWorld(t)
	claude := model.NewLLMModel(w.provider.ID(), w.org.ID(), "claude", "claude-real", "", 1000, 1000)
	claude.SetAPIType("anthropic")
	claude.SetKind(model.ModelKindEmbeddings)
	w.models["claude"] = claude
	router := NewOrgModelRouter(teamsProviderStore{w: w}, teamsOrgStore{w: w}, teamsTestSecretKey)

	req := w.request("acme/claude")
	req.Type = genaiProxy.RequestTypeEmbedding
	_, _, err := router.ResolveModel(w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, nil), req)
	require.ErrorContains(t, err, "API type 'anthropic' has no embeddings client",
		"the model's API type decides, not the openai provider's")
}
