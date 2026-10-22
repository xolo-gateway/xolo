package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/crypto"
	"github.com/xolo-gateway/xolo/internal/pipeline/pipelinetest"
)

// typesafeUpstream answers the TypeSafe evaluation endpoint and keeps the
// requests it got.
type typesafeUpstream struct {
	mu       sync.Mutex
	paths    []string
	bodies   []map[string]any
	authKeys []string
}

func (u *typesafeUpstream) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	u.mu.Lock()
	u.paths = append(u.paths, req.URL.Path)
	u.bodies = append(u.bodies, body)
	u.authKeys = append(u.authKeys, req.Header.Get("Authorization"))
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"model":"jev-2026-09","answers":{"is_urgent":{"type":"noul","noul":0.9}},"usage":{"input_tokens":40,"output_tokens":1}}`))
}

// decisionWorld is a teams world whose provider is a TypeSafe one at
// baseURL, serving the decision model "jev".
func decisionWorld(t *testing.T, baseURL string) *teamsWorld {
	t.Helper()
	w := newTeamsWorld(t)
	key, err := crypto.Encrypt(teamsTestSecretKey, "ts-key")
	require.NoError(t, err)
	w.provider = model.NewProvider(w.org.ID(), "TypeSafe", "typesafe", baseURL, key, "USD")
	jev := model.NewLLMModel(w.provider.ID(), w.org.ID(), "jev", "jev-latest", "", 1000, 4000)
	jev.SetKind(model.ModelKindDecision)
	w.models["jev"] = jev
	return w
}

func decisionRequest(w *teamsWorld, name string) *genaiProxy.ProxyRequest {
	req := w.request(name)
	req.Type = genaiProxy.RequestTypeDecision
	return req
}

func TestOrgModelRouterServesADecisionModel(t *testing.T) {
	upstream := &typesafeUpstream{}
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	w := decisionWorld(t, srv.URL+"/v1")
	router := NewOrgModelRouter(teamsProviderStore{w: w}, teamsOrgStore{w: w}, teamsTestSecretKey)
	ctx := w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, nil)

	client, _, err := router.ResolveModel(ctx, decisionRequest(w, "acme/jev"))
	require.NoError(t, err)
	require.True(t, llm.SupportsDecision(client), "the client of a decision model must decide, through its wrappers")

	res, err := llm.Decide(ctx, client, "My payouts fail.", llm.Questions{"is_urgent": llm.NoulQuestion{Instructions: "Urgent?"}})
	require.NoError(t, err)
	urgent, err := llm.AnswerOf[llm.NoulAnswer](res, "is_urgent")
	require.NoError(t, err)
	require.InDelta(t, 0.9, urgent.Noul(), 1e-9)

	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	require.Equal(t, []string{"/v1/systemone"}, upstream.paths)
	require.Equal(t, "jev-latest", upstream.bodies[0]["model"], "the real model goes upstream")
	require.Equal(t, "Bearer ts-key", upstream.authKeys[0])
}

func TestOrgModelRouterKeepsEachKindToItsRoutes(t *testing.T) {
	w := decisionWorld(t, "http://127.0.0.1:9/v1")
	w.models["gpt"] = model.NewLLMModel(w.provider.ID(), w.org.ID(), "gpt", "gpt-real", "", 1000, 1000)
	router := NewOrgModelRouter(teamsProviderStore{w: w}, teamsOrgStore{w: w}, teamsTestSecretKey)
	ctx := w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, nil)

	for name, req := range map[string]*genaiProxy.ProxyRequest{
		"chat on a decision model": w.request("acme/jev"),
		"decision on a chat model": decisionRequest(w, "acme/gpt"),
		"embeddings on a decision": func() *genaiProxy.ProxyRequest {
			r := w.request("acme/jev")
			r.Type = genaiProxy.RequestTypeEmbedding
			return r
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := router.ResolveModel(ctx, req)
			var apiErr *genaiProxy.APIError
			require.True(t, errors.As(err, &apiErr), "the refusal must be the client's error, got %v", err)
			require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
		})
	}
}

func TestPipelineRefusesADecisionOnAVirtualModel(t *testing.T) {
	w := decisionWorld(t, "http://127.0.0.1:9/v1")
	vm := model.NewVirtualModel(w.org.ID(), "assistant", "")
	vm.SetGraph(pipelinetest.NewGraph().Generator("in").ModelWithProxy("m", "jev").Sink("out").
		Edge("in", "request", "m", "request").Edge("m", "response", "out", "response").Build())
	w.vms.Add(vm)
	support := w.addTeam("support", "assistant")
	ctx := w.ctx(nil, []model.Team{support}, nil)

	req := decisionRequest(w, "acme/support/assistant")
	result, err := w.hook().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Nil(t, result)
	result, err = w.adapter().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Equal(t, "virtual_model_kind_mismatch", errorCode(t, result))
	require.Equal(t, http.StatusBadRequest, result.Response.StatusCode)
}

// enabledMiddlewares serves the organization's enabled middlewares.
type enabledMiddlewares struct {
	port.MiddlewareStore
	mws []model.Middleware
}

func (s enabledMiddlewares) ListEnabledMiddlewares(context.Context, model.OrgID) ([]model.Middleware, error) {
	return s.mws, nil
}

// A middleware serves completion models, "applies to all" included: a
// decision runs none and goes on to the router.
func TestPipelineRunsNoMiddlewareOnADecision(t *testing.T) {
	w := decisionWorld(t, "http://127.0.0.1:9/v1")
	mw := model.NewMiddleware(w.org.ID(), "pseudonymizer", "")
	mw.SetAppliesToAll(true)
	router := NewOrgModelRouter(teamsProviderStore{w: w}, teamsOrgStore{w: w}, teamsTestSecretKey)
	adapter := NewPipelineHookAdapter(pipelinetest.NewPluginProvider(), w.vms, teamsPVMStore{w: w}, teamsProviderStore{w: w}, teamsOrgStore{w: w}, enabledMiddlewares{mws: []model.Middleware{mw}}, router)

	req := decisionRequest(w, "acme/jev")
	result, err := adapter.PreRequest(w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, nil), req)
	require.NoError(t, err)
	require.Nil(t, result)
	_, hasExecution := req.Metadata[metaPipelineExecution]
	require.False(t, hasExecution, "no pipeline may run on a decision")
}

// Pipelines and plugins hold conversations: a decision model has none to
// give them.
func TestOrgModelRouterKeepsDecisionModelsOutOfPipelines(t *testing.T) {
	w := decisionWorld(t, "http://127.0.0.1:9/v1")
	router := NewOrgModelRouter(teamsProviderStore{w: w}, teamsOrgStore{w: w}, teamsTestSecretKey)

	_, _, _, err := router.ResolveRealModel(w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, nil), w.org.ID(), "jev")
	require.ErrorContains(t, err, "decision model")
}

// A decision model on an API with no decision client, after its provider
// changed type, answers a 400 that says why, as an embeddings model does.
func TestOrgModelRouterRefusesADecisionModelOnAnAPIThatCannotDecide(t *testing.T) {
	w := decisionWorld(t, "http://127.0.0.1:9/v1")
	key, err := crypto.Encrypt(teamsTestSecretKey, "key")
	require.NoError(t, err)
	w.provider = model.NewProvider(w.org.ID(), "OpenAI", "openai", "http://127.0.0.1:9/v1", key, "USD")
	router := NewOrgModelRouter(teamsProviderStore{w: w}, teamsOrgStore{w: w}, teamsTestSecretKey)

	_, _, err = router.ResolveModel(w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, nil), decisionRequest(w, "acme/jev"))
	var apiErr *genaiProxy.APIError
	require.True(t, errors.As(err, &apiErr), "the refusal must reach the client, got %v", err)
	require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	require.Contains(t, apiErr.Message, "has no decision client")
}

// A virtual model whose graph names a decision model is misconfigured: the
// chat it is asked answers a 400 naming the model, not a server error.
func TestPipelineNamingADecisionModelAnswersABadRequest(t *testing.T) {
	w := decisionWorld(t, "http://127.0.0.1:9/v1")
	vm := model.NewVirtualModel(w.org.ID(), "assistant", "")
	vm.SetGraph(pipelinetest.NewGraph().Generator("in").ModelWithProxy("m", "jev").Sink("out").
		Edge("in", "request", "m", "request").Edge("m", "response", "out", "response").Build())
	w.vms.Add(vm)
	support := w.addTeam("support", "assistant", "jev")
	ctx := w.ctx(nil, []model.Team{support}, nil)

	req := w.request("acme/support/assistant")
	result, err := w.hook().PreRequest(ctx, req)
	require.NoError(t, err)
	require.Nil(t, result)
	result, err = w.adapter().PreRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result, "the pipeline must refuse the request")
	require.Equal(t, "model_kind_mismatch", errorCode(t, result))
	require.Equal(t, http.StatusBadRequest, result.Response.StatusCode)
}
