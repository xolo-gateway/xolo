package setup

import (
	"context"
	"sync"

	"github.com/xolo-gateway/xolo/internal/config"
	"github.com/xolo-gateway/xolo/internal/plugin"
)

var (
	pluginManagerOnce sync.Once
	pluginManagerVal  *plugin.Manager
	pluginManagerErr  error
)

func getPluginManagerFromConfig(ctx context.Context, conf *config.Config) (*plugin.Manager, error) {
	pluginManagerOnce.Do(func() {
		providerStore, err := getProviderStoreFromConfig(ctx, conf)
		if err != nil {
			pluginManagerErr = err
			return
		}
		virtualModelStore, err := getVirtualModelStoreFromConfig(ctx, conf)
		if err != nil {
			pluginManagerErr = err
			return
		}
		secretStore, err := getSecretStoreFromConfig(ctx, conf)
		if err != nil {
			pluginManagerErr = err
			return
		}
		eventEmitter, err := getEventEmitterFromConfig(ctx, conf)
		if err != nil {
			pluginManagerErr = err
			return
		}
		mgr := plugin.NewManager(conf.Plugins.Dir, conf.Plugins.MemLimit, conf.Plugins.RestartCooldown, providerStore, virtualModelStore, secretStore, eventEmitter, conf.SecretKey)
		if err := mgr.Start(ctx); err != nil {
			pluginManagerErr = err
			return
		}
		pluginManagerVal = mgr
	})
	return pluginManagerVal, pluginManagerErr
}

// ShutdownPluginManager stops the plugin subprocesses, if the manager was
// started. The server calls it on its way out: the plugins are not children
// the kernel reaps with it, they outlive the server and keep running.
func ShutdownPluginManager() {
	if pluginManagerVal != nil {
		pluginManagerVal.Shutdown()
	}
}
