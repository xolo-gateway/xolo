package component

import (
	"github.com/xolo-gateway/xolo/internal/core/model"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
)

// Event scope values for the explorer.
const (
	EventScopeMine   = "mine"
	EventScopeAll    = "all"
	EventScopeGlobal = "global"
)

type EventsExplorerVModel struct {
	common.AppLayoutVModel
	Org        model.Organization
	Query      string
	Scope      string
	CanReadAll bool
	Events     []model.Event
	Error      string
	Page       int
	HasPrev    bool
	HasNext    bool
	// View carries the browsing context ("personal" or ""), preserved across links.
	View       string
	KnownTypes []model.EventTypeDef
	// Actors maps the user ids carried by Events to a displayable identity, so
	// the table shows a name rather than an opaque id. A missing entry falls
	// back to the raw id.
	Actors map[model.UserID]EventActor
}

// EventActor is the displayable identity behind an event's user id. An event
// emitted through an application's token carries that application's shadow
// user, which the table marks as such.
type EventActor struct {
	Label         string
	IsApplication bool
}

type AlertsPageVModel struct {
	common.AppLayoutVModel
	Org          model.Organization
	Alerts       []model.Alert
	CanWriteOrg  bool
	CanOwnAlerts bool
	View         string
	Success      string
}

type AlertFormVModel struct {
	common.AppLayoutVModel
	Org   model.Organization
	Alert model.Alert // nil when creating
	IsNew bool
	Scope model.AlertScope
	View  string
	Error string
	// Raw form values preserved on validation error.
	FormName        string
	FormDescription string
	FormQuery       string
	FormWindow      string
	FormComparator  string
	FormThreshold   string
	FormFor         string
	FormEnabled     bool
}

type IncidentView struct {
	Incident model.AlertIncident
	Alert    model.Alert
	Events   []model.Event
}

type IncidentsPageVModel struct {
	common.AppLayoutVModel
	Org       model.Organization
	Incidents []IncidentView
	View      string
}
