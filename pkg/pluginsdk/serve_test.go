// Copyright The Xolo Authors
// SPDX-License-Identifier: Apache-2.0

package pluginsdk_test

import (
	"context"
	"testing"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
)

// stubPlugin implements only Describe; all other methods use UnimplementedXoloPluginServer.
type stubPlugin struct {
	proto.UnimplementedXoloPluginServer
}

func (s *stubPlugin) Describe(_ context.Context, _ *proto.DescribeRequest) (*proto.PluginDescriptor, error) {
	return &proto.PluginDescriptor{Name: "stub", Version: "0.0.1"}, nil
}

func TestNoopInitWrapper_ReturnsZeroPort(t *testing.T) {
	wrapped := pluginsdk.WrapWithNoopInit(&stubPlugin{})
	resp, err := wrapped.Initialize(context.Background(), &proto.InitializeRequest{
		HostServiceBrokerId: 42,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.HttpUiPort != 0 {
		t.Errorf("expected port 0, got %d", resp.HttpUiPort)
	}
}
