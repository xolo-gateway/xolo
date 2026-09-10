// Copyright The Xolo Authors
// SPDX-License-Identifier: Apache-2.0

package pluginsdk

import "context"

type contextKey int

const (
	hostClientKey contextKey = iota
	pluginNameKey
)

// HostClientFromContext retrieves the HostClient injected by ServeWithUI middleware.
// Returns nil if not present (indicates a bug in ServeWithUI wiring).
func HostClientFromContext(ctx context.Context) HostClient {
	v, _ := ctx.Value(hostClientKey).(HostClient)
	return v
}

// PluginNameFromContext retrieves the plugin name injected by ServeWithUI middleware.
func PluginNameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(pluginNameKey).(string)
	return v
}

func contextWithHostClient(ctx context.Context, client HostClient) context.Context {
	return context.WithValue(ctx, hostClientKey, client)
}

func contextWithPluginName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, pluginNameKey, name)
}

// ContextWithHostClientForTest injecte un HostClient dans le contexte.
// Réservé aux tests des plugins (le serveur de production injecte le client
// via ServeWithUI).
func ContextWithHostClientForTest(ctx context.Context, client HostClient) context.Context {
	return contextWithHostClient(ctx, client)
}

// ContextWithPluginNameForTest injecte un nom de plugin dans le contexte.
// Réservé aux tests des plugins.
func ContextWithPluginNameForTest(ctx context.Context, name string) context.Context {
	return contextWithPluginName(ctx, name)
}
