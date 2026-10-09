package gorm

import (
	"context"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/eventql"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

// eventMemoryScanCap bounds how many rows are scanned when a query needs
// in-memory filtering (attributes, message or regex matchers).
const eventMemoryScanCap = 10000

// RecordEvent implements port.EventStore.
func (s *Store) RecordEvent(ctx context.Context, event model.Event) error {
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Create(fromEvent(event)).Error)
	})
}

// QueryEvents implements port.EventStore.
//
// Indexed labels, time range and visibility are pushed down to SQL. Attribute,
// message and regex matchers are evaluated in memory over the SQL-narrowed rows
// (Loki-style: labels are indexed, everything else is scanned).
func (s *Store) QueryEvents(ctx context.Context, filter port.EventFilter) ([]model.Event, error) {
	memoryFilter := needsMemoryFilter(filter.Query)

	var rows []*Event
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		query := applyEventFilterSQL(db.Model(&Event{}), filter)
		query = query.Order("created_at DESC")

		if memoryFilter {
			// Cap the scan; offset/limit are applied after in-memory filtering.
			query = query.Limit(eventMemoryScanCap)
		} else {
			if filter.Limit != nil {
				query = query.Limit(*filter.Limit)
			}
			if filter.Offset != nil {
				query = query.Offset(*filter.Offset)
			}
		}
		return errors.WithStack(query.Find(&rows).Error)
	})
	if err != nil {
		return nil, err
	}

	if !memoryFilter {
		return wrapEvents(rows), nil
	}

	// In-memory filtering with manual offset/limit.
	offset := 0
	if filter.Offset != nil {
		offset = *filter.Offset
	}
	limit := -1
	if filter.Limit != nil {
		limit = *filter.Limit
	}

	result := make([]model.Event, 0, len(rows))
	skipped := 0
	for _, r := range rows {
		if filter.Query != nil && !filter.Query.Match(eventFields(r)) {
			continue
		}
		if skipped < offset {
			skipped++
			continue
		}
		result = append(result, &wrappedEvent{r})
		if limit >= 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// CountEvents implements port.EventStore.
func (s *Store) CountEvents(ctx context.Context, orgID model.OrgID) (int64, error) {
	var count int64
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&Event{}).
			Where("org_id = ? AND pinned = ?", string(orgID), false).
			Count(&count).Error)
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// PinEvents implements port.EventStore.
func (s *Store) PinEvents(ctx context.Context, ids []model.EventID, incidentID model.AlertIncidentID) error {
	if len(ids) == 0 {
		return nil
	}
	strIDs := make([]string, len(ids))
	for i, id := range ids {
		strIDs[i] = string(id)
	}
	return s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Model(&Event{}).
			Where("id IN ?", strIDs).
			Updates(map[string]any{"pinned": true, "incident_id": string(incidentID)}).Error)
	})
}

// EvictOverflow implements port.EventStore.
func (s *Store) EvictOverflow(ctx context.Context, orgID model.OrgID, keepN int) (int64, error) {
	if keepN < 0 {
		keepN = 0
	}
	var affected int64
	err := s.withRetry(ctx, true, func(ctx context.Context, db *gorm.DB) error {
		// The keep-set is materialized in a derived table: PostgreSQL forbids
		// LIMIT directly inside an IN (...) subquery. The events of a frozen
		// member stay: one of them would make the guards abort the whole
		// statement, and the rest of the organization is trimmed all the
		// same. Frozen organizations and tenants are left out by
		// ListEventOrgIDs.
		result := db.Exec(`
			DELETE FROM events
			WHERE org_id = ? AND pinned = ? AND id NOT IN (
				SELECT id FROM (
					SELECT id FROM events
					WHERE org_id = ? AND pinned = ?
					ORDER BY created_at DESC
					LIMIT ?
				) AS kept
			)
			AND NOT EXISTS (SELECT 1 FROM resource_deletions d WHERE d.family = ? AND d.resource_id = events.user_id)`,
			string(orgID), false, string(orgID), false, keepN, model.FamilyMember)
		if result.Error != nil {
			return errors.WithStack(result.Error)
		}
		affected = result.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// ListEventOrgIDs implements port.EventStore.
func (s *Store) ListEventOrgIDs(ctx context.Context) ([]model.OrgID, error) {
	var ids, frozen []string
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		if err := db.Model(&Event{}).Distinct().Pluck("org_id", &ids).Error; err != nil {
			return errors.WithStack(err)
		}
		// The events of a frozen scope can not be evicted anymore.
		return errors.WithStack(db.Table("organizations o").
			Where("EXISTS (SELECT 1 FROM resource_deletions d WHERE (d.family = 'organization' AND d.resource_id = o.id) OR (d.family = 'tenant' AND d.resource_id = o.tenant_id))").
			Pluck("o.id", &frozen).Error)
	})
	if err != nil {
		return nil, err
	}
	skip := make(map[string]bool, len(frozen))
	for _, id := range frozen {
		skip[id] = true
	}
	result := make([]model.OrgID, 0, len(ids))
	for _, id := range ids {
		if !skip[id] {
			result = append(result, model.OrgID(id))
		}
	}
	return result, nil
}

// ListIncidentEvents implements port.EventStore.
func (s *Store) ListIncidentEvents(ctx context.Context, incidentID model.AlertIncidentID) ([]model.Event, error) {
	var rows []*Event
	err := s.withRetry(ctx, false, func(ctx context.Context, db *gorm.DB) error {
		return errors.WithStack(db.Where("incident_id = ?", string(incidentID)).
			Order("created_at DESC").Find(&rows).Error)
	})
	if err != nil {
		return nil, err
	}
	return wrapEvents(rows), nil
}

// needsMemoryFilter reports whether the query has matchers that cannot be pushed
// to SQL (attributes, message lines, or any regex).
func needsMemoryFilter(q *eventql.Query) bool {
	if q == nil {
		return false
	}
	if len(q.Attrs) > 0 || len(q.Lines) > 0 {
		return true
	}
	return q.HasRegex()
}

func applyEventFilterSQL(query *gorm.DB, filter port.EventFilter) *gorm.DB {
	if filter.OrgID != nil {
		query = query.Where("org_id = ?", string(*filter.OrgID))
	}

	// Visibility scoping.
	if !filter.AllUsers {
		var conds []string
		var args []any
		if filter.UserID != nil {
			conds = append(conds, "user_id = ?")
			args = append(args, string(*filter.UserID))
		}
		if filter.IncludeGlobal {
			conds = append(conds, "user_id = ''")
		}
		if len(conds) > 0 {
			query = query.Where("("+strings.Join(conds, " OR ")+")", args...)
		}
	}

	if filter.Since != nil {
		query = query.Where("created_at >= ?", *filter.Since)
	}
	if filter.Until != nil {
		query = query.Where("created_at <= ?", *filter.Until)
	}

	// Push down indexed label equality/inequality matchers (regex handled in memory).
	if filter.Query != nil {
		for _, m := range filter.Query.Labels {
			col, ok := eventLabelColumn(m.Name)
			if !ok {
				continue
			}
			switch m.Op {
			case eventql.OpEq:
				query = query.Where(col+" = ?", m.Value)
			case eventql.OpNeq:
				query = query.Where(col+" <> ?", m.Value)
			}
		}
	}

	return query
}

func wrapEvents(rows []*Event) []model.Event {
	result := make([]model.Event, 0, len(rows))
	for _, r := range rows {
		result = append(result, &wrappedEvent{r})
	}
	return result
}

var _ port.EventStore = &Store{}
