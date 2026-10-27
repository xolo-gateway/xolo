# Xolo plugin SDK

This directory is a standalone Go module,
`github.com/xolo-gateway/xolo/pkg/pluginsdk`. It contains everything a plugin
needs to talk to Xolo: the gRPC interface (`proto/plugin.proto` and its
generated code), the go-plugin handshake, and the serving helpers.

It is a separate module on purpose: a plugin author depends on this module
only, never on the Xolo server module and its dependency tree.

## Writing a plugin

Plugins are separate processes started by Xolo through
[hashicorp/go-plugin](https://github.com/hashicorp/go-plugin). A minimal
plugin implements the `XoloPlugin` gRPC service and calls `pluginsdk.Serve`:

```go
package main

import (
	"github.com/xolo-gateway/xolo/pkg/pluginsdk"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

type myPlugin struct {
	proto.UnimplementedXoloPluginServer
}

func main() {
	pluginsdk.Serve(pluginsdk.WrapWithNoopInit(&myPlugin{}))
}
```

The bundled plugins under [`plugins/`](../../plugins/) are working examples.

## Regenerating the gRPC code

From the repository root:

```bash
make generate-proto
```

Never edit `proto/*.pb.go` by hand.

## Versioning

The module is tagged from the repository root with the nested-module
convention: `pkg/pluginsdk/vX.Y.Z`. Breaking changes to `plugin.proto` go
through the RFC process described in [GOVERNANCE.md](../../GOVERNANCE.md).

## License

This module is distributed under the Apache License 2.0 ([LICENSE](LICENSE)),
unlike the rest of the repository, which is under the AGPL-3.0. A plugin that
communicates with Xolo only through the gRPC interface defined here may be
distributed under any license, including a proprietary one: see
[LICENSE-EXCEPTION](../../LICENSE-EXCEPTION) at the repository root for the
exact scope of that permission and its limits.

## Secret scopes

Use `RequestContext.SecretScopeId` for node configuration secrets and pass the
full `(scopeID, pluginName, nodeID, key)` tuple to the host client. The scope is
an organization ID or `~:<userID>` for a personal graph. `OrgId` remains the
organization used for model resolution, quotas and events; it is independent
of the graph owner. Plugin UIs receive `X-Xolo-Secret-Scope-Id` from the server.
Per-user MCP OAuth authorizations always use `~:<userID>` and `oauth:<userID>`,
including while running organization graphs. There is no fallback scope.

Rebuild and ship the host and all plugins together when upgrading this contract
(`scope_id` in secret RPCs and `secret_scope_id` in `RequestContext`).
The handshake protocol is version 3; older binaries are rejected.

## Request payload

The host sends the request body once per call. `PreRequestInput.Model` and
`ResolveModelInput.BodyJson` hold the body entering the node: the `request`
input when an upstream node feeds one, the incoming body otherwise. `PreRequestInput.MessagesJson`
and `ResolveModelInput.MessagesJson` are empty while the messages are those of
the body, and an input port that only repeats the body or the messages is left
out of `InputsJson`. Read them with `PreRequestMessages`, `ResolveModelMessages`
and `PreRequestInputs`, never from the raw fields.

## Message size

Every gRPC message between Xolo and a plugin is bounded by
`XOLO_PLUGINS_MAX_MESSAGE_SIZE` (64 MiB by default, between 1 MiB and 1 GiB). The plugin manager hands
the bound to each plugin through `XOLO_PLUGIN_MAX_MESSAGE_SIZE`; `Serve` and
`ServeWithUI` apply it.
