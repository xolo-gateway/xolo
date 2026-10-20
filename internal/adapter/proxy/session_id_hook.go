package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/bornholm/genai/llm"
	genaiProxy "github.com/bornholm/genai/proxy"
)

// SessionIDHook is a PreRequestHook (priority 1) that gives every request a
// session ID, which the genai proxy passes upstream: for sticky routing
// (OpenRouter), as the prompt cache key (Responses API), or in the session
// header a provider configures (OpenCode Go refuses a request without one).
//
// The session the client sent (x-session-id, or the header of Claude Code,
// Codex or OpenCode, read by the genai proxy) is kept as is. Otherwise an
// opaque value is derived by SHA-256 from the caller (user, organization,
// token) and the first user message: it stays the same over the turns of a
// conversation and differs from one conversation to the next.
type SessionIDHook struct{}

func (h *SessionIDHook) Name() string  { return "xolo.session-id" }
func (h *SessionIDHook) Priority() int { return 1 }

func (h *SessionIDHook) PreRequest(ctx context.Context, req *genaiProxy.ProxyRequest) (*genaiProxy.HookResult, error) {
	if req.SessionID != "" {
		return nil, nil
	}

	populateMetaFromContext(ctx, req)
	userID := req.UserID
	orgID := string(OrgIDFromMeta(req.Metadata))
	if userID == "" || orgID == "" {
		return nil, nil
	}
	req.SessionID = conversationSessionID(userID, orgID, AuthTokenIDFromMeta(req.Metadata), req.ChatOptions)
	return nil, nil
}

// conversationSessionID derives an opaque session ID from the caller and the
// first user message of the conversation, which the following turns repeat.
func conversationSessionID(userID, orgID, tokenID string, funcs []llm.ChatCompletionOptionFunc) string {
	hash := sha256.New()
	for _, part := range []string{userID, orgID, tokenID, firstUserMessage(funcs)} {
		hash.Write([]byte(part))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))[:24]
}

// firstUserMessage returns the text of the first user message of a chat
// request, which the following turns repeat, or an empty string.
func firstUserMessage(funcs []llm.ChatCompletionOptionFunc) string {
	if len(funcs) == 0 {
		return ""
	}
	for _, m := range llm.NewChatCompletionOptions(funcs...).Messages {
		if m.Role() == llm.RoleUser {
			return m.Content()
		}
	}
	return ""
}

var _ genaiProxy.PreRequestHook = &SessionIDHook{}
