package port

import (
	"context"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/eventql"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

type EventStore interface {
	RecordEvent(ctx context.Context, event model.Event) error
	QueryEvents(ctx context.Context, filter EventFilter) ([]model.Event, error)
	// CountEvents returns the number of non-pinned events stored for an org
	// (used to drive ring-buffer purge decisions).
	CountEvents(ctx context.Context, orgID model.OrgID) (int64, error)
	// PinEvents marks the given events as pinned and attaches them to the
	// incident so they survive eviction.
	PinEvents(ctx context.Context, ids []model.EventID, incidentID model.AlertIncidentID) error
	// EvictOverflow deletes non-pinned events of an org beyond the newest keepN,
	// returning the number of deleted rows.
	EvictOverflow(ctx context.Context, orgID model.OrgID, keepN int) (int64, error)
	// ListEventOrgIDs returns the distinct org IDs having stored events (used by
	// the purge loop).
	ListEventOrgIDs(ctx context.Context) ([]model.OrgID, error)
	// ListIncidentEvents returns the events pinned to an incident.
	ListIncidentEvents(ctx context.Context, incidentID model.AlertIncidentID) ([]model.Event, error)
}

// EventFilter narrows an event query. The compiled Query carries the eventql
// matchers; the visibility fields restrict which users' events are returned.
type EventFilter struct {
	OrgID *model.OrgID
	Query *eventql.Query
	Since *time.Time
	Until *time.Time

	// Visibility scoping. When AllUsers is true no user restriction is applied.
	// Otherwise events are restricted to UserID, optionally OR'd with global
	// (empty user) events when IncludeGlobal is set.
	UserID        *model.UserID
	IncludeGlobal bool
	AllUsers      bool
	// UserIDs restricts the events to these users, OR'd with UserID. A team
	// lead reads the events of its team members through it.
	UserIDs []model.UserID

	Limit  *int
	Offset *int
}
