// Copyright The Xolo Authors
// SPDX-License-Identifier: Apache-2.0

package pluginsdk

import "github.com/hashicorp/go-plugin"

const PluginName = "xolo_plugin"

// HandshakeConfig is shared between host (Xolo) and plugin binaries.
// ProtocolVersion must be incremented whenever the gRPC interface changes.
//
// MagicCookieValue is frozen on purpose and its "v1" suffix is not a version:
// the cookie only tells a plugin binary that it was launched by a host. Only
// ProtocolVersion carries a break, and a mismatch there is reported to the
// host as an incompatible API version, whereas a different cookie would make
// an outdated plugin exit as if it had been run by hand.
var HandshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  3,
	MagicCookieKey:   "XOLO_PLUGIN",
	MagicCookieValue: "xolo-plugin-v1",
}
