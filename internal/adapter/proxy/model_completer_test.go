package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/plugin"
)

// A plugin calls a model outside the proxy, which gives a request its
// session: the completer must give one itself, or a gateway requiring a
// session header (OpenCode Go) refuses the call.
func TestCompleterSendsASessionHeader(t *testing.T) {
	w := newTeamsWorld(t)
	sessions := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		sessions <- r.Header.Get("X-Opencode-Session")
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"id":"c","object":"chat.completion","model":"gpt-real","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(upstream.Close)

	provider := model.NewProvider(w.org.ID(), "opencode-go", "openai", upstream.URL, w.provider.APIKey(), "USD")
	provider.SetID(w.provider.ID())
	provider.SetRequestHeaders(&model.ProviderRequestHeaders{SessionHeader: "x-opencode-session"})
	w.provider = provider

	res, err := w.adapter().Complete(w.ctx([]string{string(rbac.PermModelUseOrg)}, nil, nil), plugin.ModelCompletionRequest{
		OrgID:     w.org.ID(),
		UserID:    w.user.ID(),
		ProxyName: "gpt",
		Messages:  []plugin.ChatMessage{{Role: "user", Content: "Classify this."}},
	})
	require.NoError(t, err)
	require.Equal(t, "ok", res.Content)
	require.NotEmpty(t, <-sessions)
}
