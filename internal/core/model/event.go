package model

import (
	"time"

	"github.com/rs/xid"
)

type EventID string

func NewEventID() EventID {
	return EventID(xid.New().String())
}

// EventSeverity classifies the importance of an event.
type EventSeverity string

const (
	SeverityInfo    EventSeverity = "info"
	SeverityWarning EventSeverity = "warning"
	SeverityError   EventSeverity = "error"
)

// EventSourcePlatform is the source value for events emitted by the platform
// itself (as opposed to a plugin, whose source is its plugin name).
const EventSourcePlatform = "platform"

// Well-known platform event types. Plugin event types are namespaced
// "plugin.<name>.<type>" server-side and are not listed here.
const (
	EventTypeProxyRequest       = "proxy.request"
	EventTypeProxyRequestFailed = "proxy.request.failed"
	EventTypeAuthLoginFailed    = "auth.login.failed"

	EventTypeProviderCreated = "provider.created"
	EventTypeProviderUpdated = "provider.updated"
	EventTypeProviderDeleted = "provider.deleted"

	EventTypeModelCreated = "model.created"
	EventTypeModelUpdated = "model.updated"
	EventTypeModelDeleted = "model.deleted"

	EventTypeVirtualModelCreated = "virtual-model.created"
	EventTypeVirtualModelUpdated = "virtual-model.updated"
	EventTypeVirtualModelDeleted = "virtual-model.deleted"

	EventTypeMiddlewareCreated = "middleware.created"
	EventTypeMiddlewareUpdated = "middleware.updated"
	EventTypeMiddlewareDeleted = "middleware.deleted"

	EventTypeRoleCreated = "role.created"
	EventTypeRoleUpdated = "role.updated"
	EventTypeRoleDeleted = "role.deleted"

	EventTypeApplicationCreated      = "application.created"
	EventTypeApplicationUpdated      = "application.updated"
	EventTypeApplicationDeleted      = "application.deleted"
	EventTypeApplicationTokenCreated = "application-token.created"
	EventTypeApplicationTokenDeleted = "application-token.deleted"

	EventTypeInviteCreated = "invite.created"
	EventTypeInviteDeleted = "invite.deleted"

	EventTypeMemberAdded   = "member.added"
	EventTypeMemberUpdated = "member.updated"
	EventTypeMemberRemoved = "member.removed"
)

// EventTypeDef describes a known event type for display and autocompletion in
// the UI.
type EventTypeDef struct {
	Type     string
	Label    string
	Severity EventSeverity
}

// PlatformEventTypes returns the catalog of event types emitted by the platform,
// used to populate query autocompletion in the UI.
func PlatformEventTypes() []EventTypeDef {
	return []EventTypeDef{
		{EventTypeProxyRequest, "Requête proxy", SeverityInfo},
		{EventTypeProxyRequestFailed, "Requête proxy en échec", SeverityWarning},
		{EventTypeAuthLoginFailed, "Échec de connexion", SeverityWarning},

		{EventTypeProviderCreated, "Fournisseur créé", SeverityInfo},
		{EventTypeProviderUpdated, "Fournisseur modifié", SeverityInfo},
		{EventTypeProviderDeleted, "Fournisseur supprimé", SeverityWarning},

		{EventTypeModelCreated, "Modèle créé", SeverityInfo},
		{EventTypeModelUpdated, "Modèle modifié", SeverityInfo},
		{EventTypeModelDeleted, "Modèle supprimé", SeverityWarning},

		{EventTypeVirtualModelCreated, "Modèle virtuel créé", SeverityInfo},
		{EventTypeVirtualModelUpdated, "Modèle virtuel modifié", SeverityInfo},
		{EventTypeVirtualModelDeleted, "Modèle virtuel supprimé", SeverityWarning},

		{EventTypeMiddlewareCreated, "Middleware créé", SeverityInfo},
		{EventTypeMiddlewareUpdated, "Middleware modifié", SeverityInfo},
		{EventTypeMiddlewareDeleted, "Middleware supprimé", SeverityWarning},

		{EventTypeRoleCreated, "Rôle créé", SeverityInfo},
		{EventTypeRoleUpdated, "Rôle modifié", SeverityInfo},
		{EventTypeRoleDeleted, "Rôle supprimé", SeverityWarning},

		{EventTypeApplicationCreated, "Application créée", SeverityInfo},
		{EventTypeApplicationUpdated, "Application modifiée", SeverityInfo},
		{EventTypeApplicationDeleted, "Application supprimée", SeverityWarning},
		{EventTypeApplicationTokenCreated, "Jeton d'application créé", SeverityInfo},
		{EventTypeApplicationTokenDeleted, "Jeton d'application supprimé", SeverityWarning},

		{EventTypeInviteCreated, "Invitation créée", SeverityInfo},
		{EventTypeInviteDeleted, "Invitation supprimée", SeverityWarning},

		{EventTypeMemberAdded, "Membre ajouté", SeverityInfo},
		{EventTypeMemberUpdated, "Rôles de membre modifiés", SeverityInfo},
		{EventTypeMemberRemoved, "Membre retiré", SeverityWarning},
	}
}

// Event is a single occurrence recorded by the event system. It may be scoped to
// an organization and optionally to a specific user; an empty OrgID denotes a
// platform-global event and an empty UserID an event not tied to a user.
type Event interface {
	WithID[EventID]

	OrgID() OrgID
	UserID() UserID
	Source() string
	Type() string
	Severity() EventSeverity
	Message() string
	Attributes() map[string]string
	// Pinned reports whether the event is retained beyond the ring-buffer
	// window because it contributed to an alert incident.
	Pinned() bool
	IncidentID() AlertIncidentID
	CreatedAt() time.Time
}

type BaseEvent struct {
	id         EventID
	orgID      OrgID
	userID     UserID
	source     string
	typ        string
	severity   EventSeverity
	message    string
	attributes map[string]string
	pinned     bool
	incidentID AlertIncidentID
	createdAt  time.Time
}

func (e *BaseEvent) ID() EventID                   { return e.id }
func (e *BaseEvent) OrgID() OrgID                  { return e.orgID }
func (e *BaseEvent) UserID() UserID                { return e.userID }
func (e *BaseEvent) Source() string               { return e.source }
func (e *BaseEvent) Type() string                 { return e.typ }
func (e *BaseEvent) Severity() EventSeverity       { return e.severity }
func (e *BaseEvent) Message() string              { return e.message }
func (e *BaseEvent) Attributes() map[string]string { return e.attributes }
func (e *BaseEvent) Pinned() bool                 { return e.pinned }
func (e *BaseEvent) IncidentID() AlertIncidentID  { return e.incidentID }
func (e *BaseEvent) CreatedAt() time.Time         { return e.createdAt }

func (e *BaseEvent) SetPinned(v bool)                 { e.pinned = v }
func (e *BaseEvent) SetIncidentID(id AlertIncidentID) { e.incidentID = id }

var _ Event = &BaseEvent{}

type EventOption func(*BaseEvent)

func WithEventOrg(orgID OrgID) EventOption   { return func(e *BaseEvent) { e.orgID = orgID } }
func WithEventUser(userID UserID) EventOption { return func(e *BaseEvent) { e.userID = userID } }
func WithEventSeverity(s EventSeverity) EventOption {
	return func(e *BaseEvent) { e.severity = s }
}
func WithEventMessage(msg string) EventOption { return func(e *BaseEvent) { e.message = msg } }

func WithEventAttributes(attrs map[string]string) EventOption {
	return func(e *BaseEvent) {
		for k, v := range attrs {
			e.attributes[k] = v
		}
	}
}

func WithEventAttribute(key, value string) EventOption {
	return func(e *BaseEvent) { e.attributes[key] = value }
}

// NewEvent creates a new event from the given source and type. Severity defaults
// to info.
func NewEvent(source, typ string, opts ...EventOption) *BaseEvent {
	e := &BaseEvent{
		id:         NewEventID(),
		source:     source,
		typ:        typ,
		severity:   SeverityInfo,
		attributes: map[string]string{},
		createdAt:  time.Now(),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}
