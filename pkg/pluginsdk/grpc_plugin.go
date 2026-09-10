// Copyright The Xolo Authors
// SPDX-License-Identifier: Apache-2.0

package pluginsdk

import (
	"context"

	"github.com/hashicorp/go-plugin"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
	"google.golang.org/grpc"
)

// brokerSetter is implemented by plugin server wrappers that need the broker (e.g. uiWrapper).
type brokerSetter interface {
	setBroker(broker *plugin.GRPCBroker)
}

// PluginClientBundle is returned by GRPCClient and exposes both the gRPC plugin client
// and the broker so the plugin manager can call Initialize and set up the host service.
type PluginClientBundle struct {
	proto.XoloPluginClient
	Broker *plugin.GRPCBroker
}

// XoloPluginGRPC implements plugin.GRPCPlugin for go-plugin.
type XoloPluginGRPC struct {
	plugin.Plugin
	// Impl is set on the plugin binary side only.
	Impl proto.XoloPluginServer
}

func (p *XoloPluginGRPC) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	// If the impl supports broker injection (e.g. uiWrapper), provide it now.
	// GRPCServer is called before any gRPC method, so the broker is always set
	// before Initialize can be called.
	if setter, ok := p.Impl.(brokerSetter); ok {
		setter.setBroker(broker)
	}
	proto.RegisterXoloPluginServer(s, p.Impl)
	return nil
}

func (p *XoloPluginGRPC) GRPCClient(_ context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return &PluginClientBundle{
		XoloPluginClient: proto.NewXoloPluginClient(c),
		Broker:           broker,
	}, nil
}

// PluginMap is the map of plugins to register on both sides.
var PluginMap = map[string]plugin.Plugin{
	PluginName: &XoloPluginGRPC{},
}
