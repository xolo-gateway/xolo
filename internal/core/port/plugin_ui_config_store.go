package port

import "context"

// PluginUIConfigStore holds the node config handed between the pipeline
// editor and a plugin's configuration UI: the editor seeds it, the plugin UI
// reads and saves it through the host service, the editor reads it back.
// These steps are separate HTTP requests, which several instances behind a
// load balancer serve in any order, so the handoff lives in the shared
// database. The scope is an organization ID or "~:<user id>", like
// SecretStore's.
type PluginUIConfigStore interface {
	SavePluginUIConfig(ctx context.Context, scopeID, pluginName, configJSON string) error
	// GetPluginUIConfig returns "" when nothing was seeded or saved.
	GetPluginUIConfig(ctx context.Context, scopeID, pluginName string) (string, error)
}
