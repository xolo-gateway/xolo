// Copyright The Xolo Authors
// SPDX-License-Identifier: Apache-2.0

package pluginsdk_test

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type echoPlugin struct {
	proto.UnimplementedXoloPluginServer
}

func (echoPlugin) PreRequest(_ context.Context, in *proto.PreRequestInput) (*proto.PreRequestOutput, error) {
	return &proto.PreRequestOutput{Allowed: true, ModifiedMessagesJson: in.GetMessagesJson()}, nil
}

// dialEcho serves echoPlugin with the given message size bound on both sides.
func dialEcho(t *testing.T, size int) proto.XoloPluginClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := pluginsdk.GRPCServerFactory(size)(nil)
	proto.RegisterXoloPluginServer(srv, echoPlugin{})
	go srv.Serve(lis) //nolint:errcheck
	t.Cleanup(srv.Stop)

	opts := append(pluginsdk.GRPCDialOptions(size),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	conn, err := grpc.NewClient("passthrough:///bufnet", opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return proto.NewXoloPluginClient(conn)
}

// TestGRPCOptions_AboveDefaultLimit sends a message above the gRPC default of
// 4 MiB both ways, as a long agent conversation does through a PreRequest.
func TestGRPCOptions_AboveDefaultLimit(t *testing.T) {
	client := dialEcho(t, pluginsdk.DefaultMaxMessageSize)
	messages := strings.Repeat("a", 5<<20)

	out, err := client.PreRequest(context.Background(), &proto.PreRequestInput{MessagesJson: messages})
	if err != nil {
		t.Fatalf("PreRequest: %v", err)
	}
	if len(out.GetModifiedMessagesJson()) != len(messages) {
		t.Fatalf("echoed %d bytes, want %d", len(out.GetModifiedMessagesJson()), len(messages))
	}
}

// TestGRPCOptions_EnforcesBound asserts that the configured bound still holds.
func TestGRPCOptions_EnforcesBound(t *testing.T) {
	client := dialEcho(t, 1<<20)

	_, err := client.PreRequest(context.Background(), &proto.PreRequestInput{MessagesJson: strings.Repeat("a", 2<<20)})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("PreRequest error = %v, want ResourceExhausted", err)
	}
}

func TestMaxMessageSizeFromEnv(t *testing.T) {
	for value, want := range map[string]int{
		"":         pluginsdk.DefaultMaxMessageSize,
		"invalid":  pluginsdk.DefaultMaxMessageSize,
		"0":        pluginsdk.DefaultMaxMessageSize,
		"-1":       pluginsdk.DefaultMaxMessageSize,
		"16777216": 16 << 20,
	} {
		t.Setenv(pluginsdk.MaxMessageSizeEnv, value)
		if got := pluginsdk.MaxMessageSizeFromEnv(); got != want {
			t.Errorf("MaxMessageSizeFromEnv() with %q = %d, want %d", value, got, want)
		}
	}
}
