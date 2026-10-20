package proxy

import (
	"context"
	"testing"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
)

func sessionRequest(token string, messages ...llm.Message) *genaiProxy.ProxyRequest {
	return &genaiProxy.ProxyRequest{
		UserID:      "user-1",
		ChatOptions: []llm.ChatCompletionOptionFunc{llm.WithMessages(messages...)},
		Metadata:    map[string]any{MetaOrgID: "org-1", MetaAuthTokenID: token},
	}
}

func sessionOf(t *testing.T, req *genaiProxy.ProxyRequest) string {
	t.Helper()
	if _, err := (&SessionIDHook{}).PreRequest(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	return req.SessionID
}

func TestSessionIDHook_KeepsTheClientSession(t *testing.T) {
	req := sessionRequest("token-1", llm.NewMessage(llm.RoleUser, "hi"))
	req.SessionID = "from-claude-code"
	if got := sessionOf(t, req); got != "from-claude-code" {
		t.Errorf("session = %q, want the client's", got)
	}
}

func TestSessionIDHook_FingerprintsTheConversation(t *testing.T) {
	question := llm.NewMessage(llm.RoleUser, "Refactor the parser.")
	first := sessionOf(t, sessionRequest("token-1", llm.NewMessage(llm.RoleSystem, "Be brief."), question))
	if first == "" {
		t.Fatal("expected a session ID")
	}

	// A later turn of the same conversation repeats its first user message.
	later := sessionOf(t, sessionRequest("token-1",
		llm.NewMessage(llm.RoleSystem, "Be brief."), question,
		llm.NewMessage(llm.RoleAssistant, "Done."),
		llm.NewMessage(llm.RoleUser, "Now add tests."),
	))
	if later != first {
		t.Errorf("a later turn got session %q, want %q", later, first)
	}

	if other := sessionOf(t, sessionRequest("token-1", llm.NewMessage(llm.RoleUser, "Write a poem."))); other == first {
		t.Error("another conversation got the same session")
	}
	if other := sessionOf(t, sessionRequest("token-2", question)); other == first {
		t.Error("another token got the same session for the same conversation")
	}
}

func TestSessionIDHook_LeavesAnAnonymousRequestAlone(t *testing.T) {
	req := &genaiProxy.ProxyRequest{Metadata: map[string]any{}}
	if got := sessionOf(t, req); got != "" {
		t.Errorf("session = %q, want none without a caller", got)
	}
}
