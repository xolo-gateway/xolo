package component

import (
	"context"
	"strings"

	"github.com/xolo-gateway/xolo/internal/i18n"

	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

// pluginCapabilityLabel turns a capability of the plugin protocol into the
// wording of the interface: the card says what a plugin can hook into, which is
// what an administrator reads it for.
func pluginCapabilityLabel(ctx context.Context, capability proto.PluginDescriptor_Capability) string {
	switch capability {
	case proto.PluginDescriptor_PRE_REQUEST:
		return i18n.T(ctx, "admin.plugins.capability.pre_request")
	case proto.PluginDescriptor_POST_RESPONSE:
		return i18n.T(ctx, "admin.plugins.capability.post_response")
	case proto.PluginDescriptor_RESOLVE_MODEL:
		return i18n.T(ctx, "admin.plugins.capability.resolve_model")
	case proto.PluginDescriptor_LIST_MODELS:
		return i18n.T(ctx, "admin.plugins.capability.list_models")
	case proto.PluginDescriptor_TOOL_PROVIDER:
		return i18n.T(ctx, "admin.plugins.capability.tool_provider")
	default:
		return strings.ToLower(capability.String())
	}
}
