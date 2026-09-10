// Copyright The Xolo Authors
// SPDX-License-Identifier: Apache-2.0

package pluginsdk

import "github.com/hashicorp/go-plugin"

const PluginName = "xolo_plugin"

// HandshakeConfig is shared between host (Xolo) and plugin binaries.
// ProtocolVersion must be incremented whenever the gRPC interface changes.
var HandshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "XOLO_PLUGIN",
	MagicCookieValue: "xolo-plugin-v1",
}
