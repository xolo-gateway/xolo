package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bornholm/genai/llm"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/crypto"
)

// stubCompletionClient answers every completion with a fixed error, or a
// response when it has none.
type stubCompletionClient struct {
	llm.Client
	err   error
	calls int
}

func (c *stubCompletionClient) ChatCompletion(context.Context, ...llm.ChatCompletionOptionFunc) (llm.ChatCompletionResponse, error) {
	c.calls++
	return nil, c.err
}

var planExhausted429 = &llm.HTTPError{StatusCode: http.StatusTooManyRequests, Body: `{"error":{"type":"rate_limit_error","message":"weekly limit reached"}}`}

func TestKeyFailoverClientMovesOffAnExhaustedKey(t *testing.T) {
	first := &stubCompletionClient{err: planExhausted429}
	second := &stubCompletionClient{}
	third := &stubCompletionClient{}
	metadata := map[string]any{}
	var exhausted []model.ProviderKeyID
	client := newKeyFailoverClient([]keyedClient{{"k1", first}, {"k2", second}, {"k3", third}}, metadata,
		func(_ context.Context, _ map[string]any, key model.ProviderKeyID, _ http.Header) {
			exhausted = append(exhausted, key)
		})

	_, err := client.ChatCompletion(context.Background())

	require.NoError(t, err)
	require.Equal(t, []int{1, 1, 0}, []int{first.calls, second.calls, third.calls})
	require.Equal(t, []model.ProviderKeyID{"k1"}, exhausted, "the key left behind goes in cooldown")
	require.Equal(t, model.ProviderKeyID("k2"), metadata[MetaProviderKeyUsed], "the usage is recorded on the key that answered")
}

func TestKeyFailoverClientKeepsTheKeyOnAnotherError(t *testing.T) {
	capacity := &llm.HTTPError{StatusCode: http.StatusTooManyRequests, Body: `{"error":{"type":"server_error","message":"the cell is at capacity"}}`}
	for name, upstreamErr := range map[string]error{
		"capacity 429": capacity,
		"server error": &llm.HTTPError{StatusCode: http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			first := &stubCompletionClient{err: upstreamErr}
			second := &stubCompletionClient{}
			metadata := map[string]any{}
			client := newKeyFailoverClient([]keyedClient{{"k1", first}, {"k2", second}}, metadata, nil)

			_, err := client.ChatCompletion(context.Background())

			require.ErrorIs(t, err, upstreamErr)
			require.Zero(t, second.calls, "an error that is not the plan's says nothing of the other keys")
			require.Equal(t, model.ProviderKeyID("k1"), metadata[MetaProviderKeyUsed])
		})
	}
}

func TestKeyFailoverClientReturnsTheLastExhaustion(t *testing.T) {
	first := &stubCompletionClient{err: planExhausted429}
	last := &stubCompletionClient{err: planExhausted429}
	metadata := map[string]any{}
	var exhausted []model.ProviderKeyID
	client := newKeyFailoverClient([]keyedClient{{"k1", first}, {"k2", last}}, metadata,
		func(_ context.Context, _ map[string]any, key model.ProviderKeyID, _ http.Header) {
			exhausted = append(exhausted, key)
		})

	_, err := client.ChatCompletion(context.Background())

	require.ErrorIs(t, err, planExhausted429)
	require.Equal(t, []model.ProviderKeyID{"k1"}, exhausted,
		"the last key's 429 reaches the error hooks, which put it in cooldown themselves")
	require.Equal(t, model.ProviderKeyID("k2"), metadata[MetaProviderKeyUsed])
}

// authRecorder answers a completion for one bearer token and a plan
// exhaustion for the others, and keeps the tokens it was called with.
type authRecorder struct {
	mu     sync.Mutex
	tokens []string
	serves string
}

func (r *authRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	r.mu.Lock()
	r.tokens = append(r.tokens, token)
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if token != r.serves {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"not this key"}}`))
		return
	}
	_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","model":"gpt-real","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
}

func TestOrgModelRouterGoesThroughTheChosenKey(t *testing.T) {
	upstream := &authRecorder{serves: "sk-member-2"}
	srv := httptest.NewServer(upstream)
	defer srv.Close()

	w := newTeamsWorld(t)
	useProviderAt(t, w, srv.URL+"/v1")
	member2, err := crypto.Encrypt(teamsTestSecretKey, "sk-member-2")
	require.NoError(t, err)
	w.provider.(*model.BaseProvider).SetExtraAPIKeys([]model.ProviderKey{{ID: "k2", Label: "member 2", APIKey: member2}})
	router := NewOrgModelRouter(teamsProviderStore{w: w}, teamsOrgStore{w: w}, teamsTestSecretKey)

	ctx := w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, nil)
	req := w.request("acme/gpt")
	req.Metadata[MetaProviderKey] = model.ProviderKeyID("k2")
	client, _, err := router.ResolveModel(ctx, req)
	require.NoError(t, err)
	_, err = client.ChatCompletion(ctx, llm.WithMessages(llm.NewMessage(llm.RoleUser, "hi")))

	require.NoError(t, err)
	require.Equal(t, []string{"sk-member-2"}, upstream.tokens, "the request goes through the key the enforcer chose")
	require.Equal(t, model.ProviderKeyID("k2"), req.Metadata[MetaProviderKeyUsed])
}
