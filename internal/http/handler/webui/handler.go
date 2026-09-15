package webui

import (
	"net/http"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/service"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/admin"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/join"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/profile"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

type pluginManagerIface interface {
	List() []*proto.PluginDescriptor
	HTTPPort(pluginName string) uint32
}

type Handler struct {
	mux                 *http.ServeMux
	inviteStore         port.InviteStore
	quotaService        *service.QuotaService
	usageStore          port.UsageStore
	userStore           port.UserStore
	orgStore            port.OrgStore
	roleStore           port.RoleStore
	providerStore       port.ProviderStore
	virtualModelStore   port.VirtualModelStore
	middlewareStore     port.MiddlewareStore
	applicationStore    port.ApplicationStore
	exchangeRateService *service.ExchangeRateService
	pluginManager       pluginManagerIface
	subscriptionMonitor port.SubscriptionMonitor
	fairShare           *service.FairShareService
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func NewHandler(
	taskRunner port.TaskRunner,
	userStore port.UserStore,
	orgStore port.OrgStore,
	roleStore port.RoleStore,
	providerStore port.ProviderStore,
	virtualModelStore port.VirtualModelStore,
	middlewareStore port.MiddlewareStore,
	personalVMStore port.PersonalVirtualModelStore,
	usageStore port.UsageStore,
	inviteStore port.InviteStore,
	applicationStore port.ApplicationStore,
	quotaStore port.QuotaStore,
	quotaService *service.QuotaService,
	exchangeRateService *service.ExchangeRateService,
	secretStore port.SecretStore,
	secretKey string,
	pluginManager pluginManagerIface,
	subscriptionMonitor port.SubscriptionMonitor,
	fairShare *service.FairShareService,
	eventStore port.EventStore,
	alertStore port.AlertStore,
	alertIncidentStore port.AlertIncidentStore,
	eventSettingsStore port.EventSettingsStore,
	eventsMaxPerOrg int,
	eventsDefaultPerOrg int,
) *Handler {
	h := &Handler{
		mux:                 http.NewServeMux(),
		inviteStore:         inviteStore,
		quotaService:        quotaService,
		usageStore:          usageStore,
		userStore:           userStore,
		orgStore:            orgStore,
		roleStore:           roleStore,
		providerStore:       providerStore,
		virtualModelStore:   virtualModelStore,
		middlewareStore:     middlewareStore,
		applicationStore:    applicationStore,
		exchangeRateService: exchangeRateService,
		pluginManager:       pluginManager,
		subscriptionMonitor: subscriptionMonitor,
		fairShare:           fairShare,
	}

	isActive := authz.Middleware(http.HandlerFunc(h.getInactiveUserPage), authz.Active())

	mount(h.mux, "/", isActive(http.HandlerFunc(h.getHomePage)))
	mount(h.mux, "/no-org", isActive(http.HandlerFunc(h.getNoOrgPage)))
	h.mux.Handle("POST /no-org/invitations/{tokenID}/decline", isActive(http.HandlerFunc(h.declineInvitation)))
	mount(h.mux, "/switcher", isActive(http.HandlerFunc(h.getSwitcherFragment)))
	mount(h.mux, "/usage", isActive(http.HandlerFunc(h.getDashboardPage)))
	mount(h.mux, "/events", isActive(http.HandlerFunc(h.getPersonalEventsRedirect)))
	hasModelAccess := authz.Middleware(http.HandlerFunc(h.getForbiddenPage), h.canAccessModelsPage())
	mount(h.mux, "/models", isActive(hasModelAccess(http.HandlerFunc(h.getModelsPage))))
	mount(h.mux, "/profile/", isActive(profile.NewHandler(userStore, orgStore, inviteStore, personalVMStore, secretStore, pluginManager)))
	mount(h.mux, "/admin/", isActive(admin.NewHandler(userStore, orgStore, roleStore, usageStore, taskRunner, exchangeRateService, pluginManager)))
	mount(h.mux, "/orgs/", isActive(org.NewHandler(orgStore, roleStore, providerStore, virtualModelStore, middlewareStore, usageStore, inviteStore, userStore, applicationStore, exchangeRateService, quotaStore, secretStore, secretKey, pluginManager, subscriptionMonitor, eventStore, alertStore, alertIncidentStore, eventSettingsStore, eventsMaxPerOrg, eventsDefaultPerOrg)))

	// Public join flow — no isActive wrapper (unauthenticated users get a sign-in prompt)
	h.mux.Handle("/join/", http.StripPrefix("/join", join.NewHandler(orgStore, roleStore, inviteStore)))

	return h
}

func mount(mux *http.ServeMux, prefix string, handler http.Handler) {
	trimmed := strings.TrimSuffix(prefix, "/")

	if len(trimmed) > 0 {
		mux.Handle(prefix, http.StripPrefix(trimmed, handler))
	} else {
		mux.Handle(prefix, handler)
	}
}

var _ http.Handler = &Handler{}
