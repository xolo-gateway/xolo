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
