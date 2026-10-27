// Copyright The Xolo Authors
// SPDX-License-Identifier: Apache-2.0

package pluginsdk

import (
	"os"
	"strconv"

	"google.golang.org/grpc"
)

// DefaultMaxMessageSize is the default bound of every gRPC message exchanged
// between Xolo and a plugin, in both directions. A PreRequest carries the
// request body, so a long agent conversation, or one with inline attachments,
// quickly outgrows the gRPC default of 4 MiB.
const DefaultMaxMessageSize = 64 << 20

// MaxMessageSizeEnv is the environment variable through which the plugin
// manager hands its message size bound to each plugin subprocess, so that
// both sides of the plugin boundary accept the same messages.
const MaxMessageSizeEnv = "XOLO_PLUGIN_MAX_MESSAGE_SIZE"

// MaxMessageSizeFromEnv returns the message size bound set by the plugin
// manager in MaxMessageSizeEnv, or DefaultMaxMessageSize when it is unset or
// invalid.
func MaxMessageSizeFromEnv() int {
	size, err := strconv.Atoi(os.Getenv(MaxMessageSizeEnv))
	if err != nil || size <= 0 {
		return DefaultMaxMessageSize
	}
	return size
}

// GRPCServerOptions bounds the messages of a gRPC server exchanging with the
// other side of the plugin boundary to size bytes.
func GRPCServerOptions(size int) []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(size),
		grpc.MaxSendMsgSize(size),
	}
}

// GRPCDialOptions bounds the messages of a gRPC client connection to the
// other side of the plugin boundary to size bytes.
func GRPCDialOptions(size int) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(size),
			grpc.MaxCallSendMsgSize(size),
		),
	}
}

// GRPCServerFactory returns a gRPC server constructor that adds the message
// size bound of GRPCServerOptions to the options it is given. It fits
// plugin.ServeConfig.GRPCServer and GRPCBroker.AcceptAndServe.
func GRPCServerFactory(size int) func([]grpc.ServerOption) *grpc.Server {
	return func(opts []grpc.ServerOption) *grpc.Server {
		return grpc.NewServer(append(opts, GRPCServerOptions(size)...)...)
	}
}
