package org

import (
	"net/http"

	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	"github.com/xolo-gateway/xolo/internal/core/service"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
	proto "github.com/xolo-gateway/xolo/pkg/pluginsdk/proto"
)

type pluginManagerIface interface {
	List() []*proto.PluginDescriptor
	HTTPPort(pluginName string) uint32
}

// Handler serves the org-admin section: /orgs/{orgSlug}/admin/
type Handler struct {
	mux                 *http.ServeMux
	orgStore            port.OrgStore
	roleStore           port.RoleStore
	providerStore       port.ProviderStore
	virtualModelStore   port.VirtualModelStore
	middlewareStore     port.MiddlewareStore
	usageStore          port.UsageStore
	inviteStore         port.InviteStore
	invitationService   *service.InvitationService
	userStore           port.UserStore
	applicationStore    port.ApplicationStore
	quotaStore          port.QuotaStore
	secretStore         port.SecretStore
	secretKey           string
	exchangeRateService *service.ExchangeRateService
	pluginManager       pluginManagerIface
	subscriptionMonitor port.SubscriptionMonitor
	eventStore          port.EventStore
	alertStore          port.AlertStore
	alertIncidentStore  port.AlertIncidentStore
	eventSettingsStore  port.EventSettingsStore
	eventsMaxPerOrg     int
	eventsDefaultPerOrg int
	// teamStore enables the team pages, see SetTeams.
	teamStore port.TeamStore
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func NewHandler(
	orgStore port.OrgStore,
	roleStore port.RoleStore,
	providerStore port.ProviderStore,
	virtualModelStore port.VirtualModelStore,
	middlewareStore port.MiddlewareStore,
	usageStore port.UsageStore,
	inviteStore port.InviteStore,
	invitationService *service.InvitationService,
	userStore port.UserStore,
	applicationStore port.ApplicationStore,
	exchangeRateService *service.ExchangeRateService,
	quotaStore port.QuotaStore,
	secretStore port.SecretStore,
	secretKey string,
	pluginManager pluginManagerIface,
	subscriptionMonitor port.SubscriptionMonitor,
	eventStore port.EventStore,
	alertStore port.AlertStore,
	alertIncidentStore port.AlertIncidentStore,
	eventSettingsStore port.EventSettingsStore,
	eventsMaxPerOrg int,
	eventsDefaultPerOrg int,
) *Handler {
	h := &Handler{
		mux:                 http.NewServeMux(),
		orgStore:            orgStore,
		roleStore:           roleStore,
		providerStore:       providerStore,
		virtualModelStore:   virtualModelStore,
		middlewareStore:     middlewareStore,
		usageStore:          usageStore,
		inviteStore:         inviteStore,
		invitationService:   invitationService,
		userStore:           userStore,
		applicationStore:    applicationStore,
		quotaStore:          quotaStore,
		secretStore:         secretStore,
		secretKey:           secretKey,
		exchangeRateService: exchangeRateService,
		pluginManager:       pluginManager,
		subscriptionMonitor: subscriptionMonitor,
		eventStore:          eventStore,
		alertStore:          alertStore,
		alertIncidentStore:  alertIncidentStore,
		eventSettingsStore:  eventSettingsStore,
		eventsMaxPerOrg:     eventsMaxPerOrg,
		eventsDefaultPerOrg: eventsDefaultPerOrg,
	}

	// assertPerm gates a route on a single RBAC permission resolved for the
	// org identified by the {orgSlug} path value.
	assertPerm := func(perm rbac.Permission) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				orgSlug := r.PathValue("orgSlug")
				ctx := r.Context()
				allowed, err := authz.Assert(ctx, httpCtx.User(ctx), h.hasPermission(orgSlug, perm))
				if err != nil {
					writeResourceLookupError(ctx, w, err)
					return
				}
				if !allowed {
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
		}
	}

	// assertAnyPerm gates a route on holding at least one of the given permissions.
	assertAnyPerm := func(perms ...rbac.Permission) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				orgSlug := r.PathValue("orgSlug")
				authz.Middleware(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						http.Error(w, "Forbidden", http.StatusForbidden)
					}),
					h.hasAnyPermission(orgSlug, perms...),
				)(next).ServeHTTP(w, r)
			})
		}
	}

	assertOrgMember := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			orgSlug := r.PathValue("orgSlug")
			authz.Middleware(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "Forbidden", http.StatusForbidden)
				}),
				h.hasOrgMembership(orgSlug),
			)(next).ServeHTTP(w, r)
		})
	}

	// Org admin routes — redirect /{orgSlug}/admin/ to /{orgSlug}/usage
	h.mux.Handle("GET /{orgSlug}/admin/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orgSlug := r.PathValue("orgSlug")
		http.Redirect(w, r, "/orgs/"+orgSlug+"/usage", http.StatusMovedPermanently)
	}))
	// Redirect /{orgSlug} to /{orgSlug}/usage
	h.mux.Handle("GET /{orgSlug}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orgSlug := r.PathValue("orgSlug")
		http.Redirect(w, r, "/orgs/"+orgSlug+"/usage", http.StatusMovedPermanently)
	}))

	h.mux.Handle("GET /{orgSlug}/admin/members", assertPerm(rbac.PermMembersRead)(http.HandlerFunc(h.getMembersPage)))
	h.mux.Handle("DELETE /{orgSlug}/admin/members/{membershipID}", assertPerm(rbac.PermMembersWrite)(http.HandlerFunc(h.deleteMember)))
	h.mux.Handle("GET /{orgSlug}/admin/members/{membershipID}/edit", assertPerm(rbac.PermMembersRead)(http.HandlerFunc(h.getEditMemberPage)))
	h.mux.Handle("POST /{orgSlug}/admin/members/{membershipID}/edit", assertPerm(rbac.PermMembersWrite)(http.HandlerFunc(h.postEditMember)))

	h.mux.Handle("GET /{orgSlug}/admin/roles", assertPerm(rbac.PermRolesRead)(http.HandlerFunc(h.getRolesPage)))
	h.mux.Handle("GET /{orgSlug}/admin/roles/new", assertPerm(rbac.PermRolesWrite)(http.HandlerFunc(h.getNewRolePage)))
	h.mux.Handle("POST /{orgSlug}/admin/roles", assertPerm(rbac.PermRolesWrite)(http.HandlerFunc(h.createRole)))
	h.mux.Handle("GET /{orgSlug}/admin/roles/{roleID}/edit", assertPerm(rbac.PermRolesRead)(http.HandlerFunc(h.getEditRolePage)))
	h.mux.Handle("POST /{orgSlug}/admin/roles/{roleID}/edit", assertPerm(rbac.PermRolesWrite)(http.HandlerFunc(h.updateRole)))
	h.mux.Handle("POST /{orgSlug}/admin/roles/{roleID}/permissions", assertPerm(rbac.PermRolesWrite)(http.HandlerFunc(h.toggleRolePermission)))
	h.mux.Handle("DELETE /{orgSlug}/admin/roles/{roleID}", assertPerm(rbac.PermRolesWrite)(http.HandlerFunc(h.deleteRole)))

	h.mux.Handle("GET /{orgSlug}/admin/providers", assertPerm(rbac.PermProvidersRead)(http.HandlerFunc(h.getProvidersPage)))
	h.mux.Handle("GET /{orgSlug}/admin/providers/new", assertPerm(rbac.PermProvidersWrite)(http.HandlerFunc(h.getNewProviderPage)))
	h.mux.Handle("POST /{orgSlug}/admin/providers", assertPerm(rbac.PermProvidersWrite)(http.HandlerFunc(h.createProvider)))
	h.mux.Handle("GET /{orgSlug}/admin/providers/{providerID}/edit", assertPerm(rbac.PermProvidersRead)(http.HandlerFunc(h.getEditProviderPage)))
	h.mux.Handle("POST /{orgSlug}/admin/providers/{providerID}/edit", assertPerm(rbac.PermProvidersWrite)(http.HandlerFunc(h.updateProvider)))
	h.mux.Handle("DELETE /{orgSlug}/admin/providers/{providerID}", assertPerm(rbac.PermProvidersWrite)(http.HandlerFunc(h.deleteProvider)))
	h.mux.Handle("POST /{orgSlug}/admin/providers/{providerID}/test", assertPerm(rbac.PermProvidersWrite)(http.HandlerFunc(h.testProvider)))

	h.mux.Handle("GET /{orgSlug}/admin/providers/{providerID}/models", assertPerm(rbac.PermProvidersRead)(http.HandlerFunc(h.getModelsPage)))
	h.mux.Handle("GET /{orgSlug}/admin/providers/{providerID}/models/new", assertPerm(rbac.PermProvidersWrite)(http.HandlerFunc(h.getNewModelPage)))
	h.mux.Handle("POST /{orgSlug}/admin/providers/{providerID}/models", assertPerm(rbac.PermProvidersWrite)(http.HandlerFunc(h.createModel)))
	h.mux.Handle("GET /{orgSlug}/admin/providers/{providerID}/models/{modelID}/edit", assertPerm(rbac.PermProvidersRead)(http.HandlerFunc(h.getEditModelPage)))
	h.mux.Handle("POST /{orgSlug}/admin/providers/{providerID}/models/{modelID}/edit", assertPerm(rbac.PermProvidersWrite)(http.HandlerFunc(h.updateModel)))
	h.mux.Handle("DELETE /{orgSlug}/admin/providers/{providerID}/models/{modelID}", assertPerm(rbac.PermProvidersWrite)(http.HandlerFunc(h.deleteModel)))

	h.mux.Handle("GET /{orgSlug}/admin/quota", assertPerm(rbac.PermQuotaRead)(http.HandlerFunc(h.getOrgQuotaPage)))
	h.mux.Handle("POST /{orgSlug}/admin/quota", assertPerm(rbac.PermQuotaWrite)(http.HandlerFunc(h.saveOrgQuota)))
	h.mux.Handle("GET /{orgSlug}/admin/members/{membershipID}/quota", assertPerm(rbac.PermQuotaRead)(http.HandlerFunc(h.getMemberQuotaPage)))
	h.mux.Handle("POST /{orgSlug}/admin/members/{membershipID}/quota", assertPerm(rbac.PermQuotaWrite)(http.HandlerFunc(h.saveMemberQuota)))

	h.mountTeams()

	// The invitation permissions can be held on a team only: the invitations
	// then make their invitee join that team, as a member (see inviteScope).
	h.mux.Handle("GET /{orgSlug}/admin/invites", http.HandlerFunc(h.getInvitesPage))
	h.mux.Handle("GET /{orgSlug}/admin/invites/new", http.HandlerFunc(h.getNewInvitePage))
	h.mux.Handle("POST /{orgSlug}/admin/invites", http.HandlerFunc(h.createInvite))
	h.mux.Handle("DELETE /{orgSlug}/admin/invites/{inviteID}", http.HandlerFunc(h.revokeInvite))
	h.mux.Handle("DELETE /{orgSlug}/admin/invites/{inviteID}/delete", http.HandlerFunc(h.deleteInvite))

	// usage:read held on the organization shows every request; held on teams
	// only, the requests of those teams (see usageScope).
	h.mux.Handle("GET /{orgSlug}/usage", http.HandlerFunc(h.getUsagePage))

	h.mux.Handle("GET /{orgSlug}/admin/settings", assertPerm(rbac.PermSettingsRead)(http.HandlerFunc(h.getSettingsPage)))
	h.mux.Handle("POST /{orgSlug}/admin/settings", assertPerm(rbac.PermSettingsWrite)(http.HandlerFunc(h.saveSettings)))

	h.mux.Handle("GET /{orgSlug}/admin/virtual-models", assertPerm(rbac.PermVirtualModelsRead)(http.HandlerFunc(h.getVirtualModelsPage)))
	h.mux.Handle("GET /{orgSlug}/admin/virtual-models/new", assertPerm(rbac.PermVirtualModelsWrite)(http.HandlerFunc(h.getNewVirtualModelPage)))
	h.mux.Handle("POST /{orgSlug}/admin/virtual-models", assertPerm(rbac.PermVirtualModelsWrite)(http.HandlerFunc(h.createVirtualModel)))
	h.mux.Handle("GET /{orgSlug}/admin/virtual-models/{modelID}/edit", assertPerm(rbac.PermVirtualModelsRead)(http.HandlerFunc(h.getEditVirtualModelPage)))
	h.mux.Handle("POST /{orgSlug}/admin/virtual-models/{modelID}/edit", assertPerm(rbac.PermVirtualModelsWrite)(http.HandlerFunc(h.updateVirtualModel)))
	h.mux.Handle("DELETE /{orgSlug}/admin/virtual-models/{modelID}", assertPerm(rbac.PermVirtualModelsWrite)(http.HandlerFunc(h.deleteVirtualModel)))
	h.mux.Handle("GET /{orgSlug}/admin/virtual-models/{modelID}/pipeline", assertPerm(rbac.PermVirtualModelsRead)(http.HandlerFunc(h.getPipelineEditorPage)))

	h.mux.Handle("GET /{orgSlug}/admin/middlewares", assertPerm(rbac.PermMiddlewaresRead)(http.HandlerFunc(h.getMiddlewaresPage)))
	h.mux.Handle("GET /{orgSlug}/admin/middlewares/new", assertPerm(rbac.PermMiddlewaresWrite)(http.HandlerFunc(h.getNewMiddlewarePage)))
	h.mux.Handle("POST /{orgSlug}/admin/middlewares", assertPerm(rbac.PermMiddlewaresWrite)(http.HandlerFunc(h.createMiddleware)))
	h.mux.Handle("GET /{orgSlug}/admin/middlewares/{middlewareID}/edit", assertPerm(rbac.PermMiddlewaresRead)(http.HandlerFunc(h.getEditMiddlewarePage)))
	h.mux.Handle("POST /{orgSlug}/admin/middlewares/{middlewareID}/edit", assertPerm(rbac.PermMiddlewaresWrite)(http.HandlerFunc(h.updateMiddleware)))
	h.mux.Handle("POST /{orgSlug}/admin/middlewares/{middlewareID}/toggle", assertPerm(rbac.PermMiddlewaresWrite)(http.HandlerFunc(h.toggleMiddleware)))
	h.mux.Handle("DELETE /{orgSlug}/admin/middlewares/{middlewareID}", assertPerm(rbac.PermMiddlewaresWrite)(http.HandlerFunc(h.deleteMiddleware)))
	h.mux.Handle("GET /{orgSlug}/admin/middlewares/{middlewareID}/pipeline", assertPerm(rbac.PermMiddlewaresRead)(http.HandlerFunc(h.getMiddlewarePipelineEditorPage)))

	h.mux.Handle("GET /{orgSlug}/admin/applications", assertPerm(rbac.PermApplicationsRead)(http.HandlerFunc(h.getApplicationsPage)))
	h.mux.Handle("GET /{orgSlug}/admin/applications/new", assertPerm(rbac.PermApplicationsWrite)(http.HandlerFunc(h.getNewApplicationPage)))
	h.mux.Handle("POST /{orgSlug}/admin/applications", assertPerm(rbac.PermApplicationsWrite)(http.HandlerFunc(h.createApplication)))
	h.mux.Handle("GET /{orgSlug}/admin/applications/{appID}/edit", assertPerm(rbac.PermApplicationsRead)(http.HandlerFunc(h.getEditApplicationPage)))
	h.mux.Handle("POST /{orgSlug}/admin/applications/{appID}/edit", assertPerm(rbac.PermApplicationsWrite)(http.HandlerFunc(h.updateApplication)))
	h.mux.Handle("POST /{orgSlug}/admin/applications/{appID}/delete", assertPerm(rbac.PermApplicationsWrite)(http.HandlerFunc(h.deleteApplication)))
	h.mux.Handle("POST /{orgSlug}/admin/applications/{appID}/tokens", assertPerm(rbac.PermApplicationsWrite)(http.HandlerFunc(h.createApplicationToken)))
	h.mux.Handle("POST /{orgSlug}/admin/applications/{appID}/tokens/{tokenID}/delete", assertPerm(rbac.PermApplicationsWrite)(http.HandlerFunc(h.deleteApplicationToken)))

	// Per-application quota editor (issue #64). The application handler above
	// already isolates an app to its org; the permission gate below also
	// covers the cross-org case where the URL slugs disagree.
	h.mux.Handle("GET /{orgSlug}/admin/applications/{appID}/quota", assertPerm(rbac.PermQuotaRead)(http.HandlerFunc(h.getApplicationQuotaPage)))
	h.mux.Handle("POST /{orgSlug}/admin/applications/{appID}/quota", assertPerm(rbac.PermQuotaWrite)(http.HandlerFunc(h.saveApplicationQuota)))

	// Plugin HTTP UI proxy — tied to virtual model / pipeline administration.
	h.mux.Handle("/{orgSlug}/plugins/{pluginName}/ui/{uiPath...}", assertPerm(rbac.PermVirtualModelsWrite)(http.HandlerFunc(h.servePluginUI)))

	// Events explorer — accessible to any org member (own events by default).
	h.mux.Handle("GET /{orgSlug}/events", assertOrgMember(http.HandlerFunc(h.getEventsExplorerPage)))

	// Alerts & incidents — accessible with either org-wide (events:write) or
	// personal (events:alerts:own) alert permission; per-alert authorization is
	// enforced in the handlers based on scope and ownership.
	alertsPerm := assertAnyPerm(rbac.PermEventsWrite, rbac.PermEventsAlertsOwn)
	h.mux.Handle("GET /{orgSlug}/events/alerts", alertsPerm(http.HandlerFunc(h.getAlertsPage)))
	h.mux.Handle("GET /{orgSlug}/events/alerts/new", alertsPerm(http.HandlerFunc(h.getNewAlertPage)))
	h.mux.Handle("POST /{orgSlug}/events/alerts", alertsPerm(http.HandlerFunc(h.createAlert)))
	h.mux.Handle("GET /{orgSlug}/events/alerts/{alertID}/edit", alertsPerm(http.HandlerFunc(h.getEditAlertPage)))
	h.mux.Handle("POST /{orgSlug}/events/alerts/{alertID}/edit", alertsPerm(http.HandlerFunc(h.updateAlert)))
	h.mux.Handle("POST /{orgSlug}/events/alerts/{alertID}/delete", alertsPerm(http.HandlerFunc(h.deleteAlert)))
	h.mux.Handle("GET /{orgSlug}/events/incidents", alertsPerm(http.HandlerFunc(h.getIncidentsPage)))

	return h
}

var _ http.Handler = &Handler{}
