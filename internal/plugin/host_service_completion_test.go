package plugin

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// failingCompleter fails every completion with err.
type failingCompleter struct{ err error }

func (c failingCompleter) Complete(context.Context, ModelCompletionRequest) (*ModelCompletionResult, error) {
	return nil, c.err
}

// A plugin calling a model that holds no conversation made a configuration
// mistake it must not retry: it is told so, apart from an upstream outage.
func TestChatCompletion_TellsAModelOfAnotherKindFromAnOutage(t *testing.T) {
	req := &proto.HostChatCompletionRequest{
		OrgId:    "org",
		Model:    "vectors",
		Messages: []*proto.ChatMessage{{Role: "user", Content: "hi"}},
	}
	for name, tc := range map[string]struct {
		err  error
		code codes.Code
	}{
		"model of another kind": {fmt.Errorf("resolve model %q: %w", "vectors", model.ErrNotAConversationModel), codes.InvalidArgument},
		"upstream outage":       {errors.New("connection refused"), codes.Internal},
	} {
		t.Run(name, func(t *testing.T) {
			host := NewXoloHostService(nil, nil, nil, nil, "")
			host.SetModelCompleter(failingCompleter{err: tc.err})

			_, err := host.ChatCompletion(context.Background(), req)
			require.Equal(t, tc.code, status.Code(err))
		})
	}
}
