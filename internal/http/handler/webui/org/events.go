package org

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/slogx"
	"github.com/xolo-gateway/xolo/internal/core/eventql"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/org/component"
)

const eventsExplorerPageSize = 50

// personalView reports whether the events pages are browsed from the personal
// menu (view=personal) rather than the org-admin section.
func personalView(r *http.Request) bool {
	return r.URL.Query().Get("view") == "personal"
}

// viewParam returns the query value carrying the browsing context through links.
func viewParam(personal bool) string {
	if personal {
		return "personal"
	}
	return ""
}

// events sidebar keys. Alerts and incidents share the "Alertes & incidents"
// entry, the events explorer has its own.
const (
	eventsNavEvents = "events"
	eventsNavAlerts = "alerts"
)

// eventsLayout builds the AppLayoutVModel for a shared events page. The same
// pages are reachable from the org-admin menu (org layout) and from the personal
// menu (personal layout, marked with ?view=personal); the layout follows the
// entry point so the user never switches section unexpectedly. nav selects which
// org sidebar entry is highlighted, and tailCrumbs are appended after the
// context root breadcrumb.
func (h *Handler) eventsLayout(org model.Organization, user model.User, personal bool, nav string, tailCrumbs []common.BreadcrumbItem) common.AppLayoutVModel {
	if !personal {
		crumbs := append([]common.BreadcrumbItem{
			{Label: org.Name(), Href: "/orgs/" + org.Slug() + "/usage"},
		}, tailCrumbs...)
		return common.AppLayoutVModel{
			User:         user,
			SelectedItem: "org-" + org.Slug() + "-" + nav,
			Context:      common.ContextOrg,
			ContextName:  org.Name(),
			ContextSlug:  org.Slug(),
			ContextOrgID: org.ID(),
			Breadcrumbs:  crumbs,
		}
	}

	crumbs := append([]common.BreadcrumbItem{
		{Label: "Espace personnel", Href: "/usage"},
	}, tailCrumbs...)
	return common.AppLayoutVModel{
		User:         user,
		SelectedItem: "events",
		Breadcrumbs:  crumbs,
		Context:      common.ContextPersonal,
	}
}

func (h *Handler) getEventsExplorerPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	canReadAll, err := h.hasPermission(orgSlug, rbac.PermEventsReadAll)(ctx, user)
	if err != nil {
		slog.ErrorContext(ctx, "could not resolve events permission", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	queryStr := r.URL.Query().Get("query")
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = component.EventScopeMine
	}
	// A user without the read:all permission is always restricted to their own events.
	if !canReadAll && scope != component.EventScopeMine {
		scope = component.EventScopeMine
	}

	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if n, convErr := strconv.Atoi(p); convErr == nil && n > 1 {
			page = n
		}
	}

	orgID := org.ID()
	offset := (page - 1) * eventsExplorerPageSize
	// Fetch one extra row to detect whether a next page exists (avoids a COUNT).
	limit := eventsExplorerPageSize + 1
	filter := port.EventFilter{OrgID: &orgID, Limit: &limit, Offset: &offset}

	switch scope {
	case component.EventScopeAll:
		filter.AllUsers = true
	case component.EventScopeGlobal:
		filter.IncludeGlobal = true // no UserID → only global (empty-user) events
	default:
		uid := user.ID()
		filter.UserID = &uid
	}

	var (
		errStr string
		events []model.Event
	)
	if queryStr != "" {
		compiled, cerr := eventql.Compile(queryStr)
		if cerr != nil {
			errStr = cerr.Error()
		} else {
			filter.Query = compiled
		}
	}

	hasNext := false
	if errStr == "" {
		events, err = h.eventStore.QueryEvents(ctx, filter)
		if err != nil {
			slog.ErrorContext(ctx, "could not query events", slogx.Error(err))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		if len(events) > eventsExplorerPageSize {
			hasNext = true
			events = events[:eventsExplorerPageSize]
		}
	}

	personal := personalView(r)
	vmodel := component.EventsExplorerVModel{
		Org:             org,
		Actors:          h.eventActors(ctx, events),
		Query:           queryStr,
		Scope:           scope,
		CanReadAll:      canReadAll,
		Events:          events,
		Error:           errStr,
		Page:            page,
		HasPrev:         page > 1,
		HasNext:         hasNext,
		View:            viewParam(personal),
		KnownTypes:      model.PlatformEventTypes(),
		AppLayoutVModel: h.eventsLayout(org, user, personal, eventsNavEvents, []common.BreadcrumbItem{{Label: "Événements", Href: ""}}),
	}

	templ.Handler(component.EventsExplorerPage(vmodel)).ServeHTTP(w, r)
}

// eventActors resolves the user ids of a page of events to displayable
// identities. Only the distinct ids of the page are looked up (at most
// eventsExplorerPageSize), and the user store is LRU-cached, so the cost stays
// negligible next to the event query itself.
func (h *Handler) eventActors(ctx context.Context, events []model.Event) map[model.UserID]component.EventActor {
	actors := make(map[model.UserID]component.EventActor, len(events))
	for _, e := range events {
		uid := e.UserID()
		if uid == "" {
			continue
		}
		if _, ok := actors[uid]; ok {
			continue
		}
		u, err := h.userStore.GetUserByID(ctx, uid)
		if err != nil {
			slog.WarnContext(ctx, "could not fetch user for event", slogx.Error(err), slog.String("userID", string(uid)))
			continue
		}
		actors[uid] = component.EventActor{
			Label:         userDisplayLabel(u),
			IsApplication: u.Provider() == model.ApplicationProvider,
		}
	}
	return actors
}

// userDisplayLabel picks the most meaningful name available for a user. An
// application's shadow user carries the application name as display name, kept
// in sync on every authenticated request.
func userDisplayLabel(u model.User) string {
	if name := u.DisplayName(); name != "" {
		return name
	}
	if email := u.Email(); email != "" {
		return email
	}
	return string(u.ID())
}

func (h *Handler) getIncidentsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := httpCtx.User(ctx)
	orgSlug := r.PathValue("orgSlug")

	org, err := h.orgFromSlug(ctx, orgSlug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	orgID := org.ID()
	incidents, err := h.alertIncidentStore.ListIncidents(ctx, port.IncidentFilter{OrgID: &orgID})
	if err != nil {
		slog.ErrorContext(ctx, "could not list incidents", slogx.Error(err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// Only show incidents whose alert the user is allowed to see (org-wide alerts
	// require events:write; personal alerts belong to their owner).
	canWriteOrg, _ := h.alertPerms(ctx, orgSlug, user)
	views := make([]component.IncidentView, 0, len(incidents))
	for _, inc := range incidents {
		alert, err := h.alertStore.GetAlertByID(ctx, inc.AlertID())
		if err != nil || !canManageAlert(alert, user, canWriteOrg) {
			continue
		}
		view := component.IncidentView{Incident: inc, Alert: alert}
		if events, err := h.eventStore.ListIncidentEvents(ctx, inc.ID()); err == nil {
			view.Events = events
		}
		views = append(views, view)
	}

	personal := personalView(r)
	vmodel := component.IncidentsPageVModel{
		Org:             org,
		Incidents:       views,
		View:            viewParam(personal),
		AppLayoutVModel: h.eventsLayout(org, user, personal, eventsNavAlerts, []common.BreadcrumbItem{{Label: "Incidents", Href: ""}}),
	}

	templ.Handler(component.IncidentsPage(vmodel)).ServeHTTP(w, r)
}
