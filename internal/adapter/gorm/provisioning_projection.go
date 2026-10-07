package gorm

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProvisioningProjection is the public representation of one resource of the
// common contract. Revision is the feed sequence that last changed it.
type ProvisioningProjection struct {
	Family         string `gorm:"primaryKey"`
	TenantID       string `gorm:"primaryKey"`
	OrgID          string `gorm:"primaryKey"`
	ResourceKey    string `gorm:"primaryKey"`
	Reference      string `gorm:"type:text;not null"`
	Representation string `gorm:"type:text;not null"`
	Revision       int64  `gorm:"not null"`
}

// ProvisioningEvent is one entry of the feed. Sequence orders commits;
// CreatedAt only serves retention and the informative CloudEvents time.
type ProvisioningEvent struct {
	Sequence    int64     `gorm:"primaryKey;autoIncrement:false;index:idx_provisioning_events_tenant_sequence,priority:2"`
	CreatedAt   time.Time `gorm:"autoCreateTime:false;not null"`
	TenantID    string    `gorm:"index;index:idx_provisioning_events_tenant_sequence,priority:1"`
	OrgID       string
	Family      string `gorm:"not null"`
	ResourceKey string `gorm:"not null"`
	Payload     string `gorm:"type:text;not null"`
}

// ProvisioningFeed holds the identity of the feed: its CloudEvents source, the
// key signing its cursors, the retention floor and, on SQLite, the sequence
// counter. PostgreSQL allocates sequences from provisioningEventSequence.
type ProvisioningFeed struct {
	ID           int    `gorm:"primaryKey;autoIncrement:false"`
	Source       string `gorm:"not null"`
	CursorSecret string `gorm:"not null"`
	Floor        int64  `gorm:"not null;default:0"`
	Head         int64  `gorm:"not null;default:0"`
}

const (
	provisioningFeedID        = 1
	provisioningEventSequence = "provisioning_event_seq"
	// provisioningFeedLock serializes publications on PostgreSQL. It is taken
	// at the end of a transaction that changes a projection, and held until
	// commit, so sequences are allocated in commit order.
	provisioningFeedLock = 867530903
)

// projectionFamilies orders families from parents to children.
var projectionFamilies = map[string]int{
	model.FamilyTenant:                 0,
	model.FamilyTenantDomain:           1,
	model.FamilyOrganization:           2,
	model.FamilyMember:                 3,
	model.FamilyOrganizationMembership: 4,
}

type projectionID struct{ family, tenantID, orgID, key string }

func (id projectionID) less(other projectionID) bool {
	if id.family != other.family {
		return projectionFamilies[id.family] < projectionFamilies[other.family]
	}
	if id.tenantID != other.tenantID {
		return id.tenantID < other.tenantID
	}
	if id.orgID != other.orgID {
		return id.orgID < other.orgID
	}
	return id.key < other.key
}

func (id projectionID) reference() model.CommonKey {
	ref := model.CommonKey{TenantID: id.tenantID}
	switch id.family {
	case model.FamilyTenantDomain:
		ref.Hostname = id.key
	case model.FamilyOrganization:
		ref.OrganizationID = id.key
	case model.FamilyMember:
		ref.MemberID = id.key
	case model.FamilyOrganizationMembership:
		ref.OrganizationID, ref.MemberID = id.orgID, id.key
	}
	return ref
}

type snapshotRow map[string]any

func (r snapshotRow) str(k string) string { v, _ := r[k].(string); return v }

func decodeSnapshot(raw []byte) (snapshotRow, error) {
	var row snapshotRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	return row, nil
}

// projectionIdentity designates the projection a resource snapshot belongs
// to, without reading anything. Application shadow users and roles are not
// part of the common contract.
func projectionIdentity(key mutationKey, row snapshotRow) (projectionID, bool) {
	if row == nil {
		return projectionID{}, false
	}
	switch key.kind {
	case "tenant":
		return projectionID{model.FamilyTenant, key.id, "", key.id}, true
	case "domain":
		return projectionID{model.FamilyTenantDomain, row.str("tenant_id"), "", key.id}, true
	case "organization":
		return projectionID{model.FamilyOrganization, row.str("tenant_id"), "", key.id}, true
	case "user":
		if row.str("provider") == model.ApplicationProvider {
			return projectionID{}, false
		}
		return projectionID{model.FamilyMember, row.str("tenant_id"), "", key.id}, true
	case "membership":
		return projectionID{model.FamilyOrganizationMembership, row.str("tenant_id"), row.str("org_id"), row.str("user_id")}, true
	}
	return projectionID{}, false
}

// projectionRepresentation builds the representation published for a resource
// snapshot. Maps marshal with sorted keys, so equal states give equal bytes.
func projectionRepresentation(id projectionID, row snapshotRow) (string, error) {
	status := row.str("status")
	if id.family == model.FamilyTenant || id.family == model.FamilyOrganization || id.family == model.FamilyMember {
		active, _ := row["active"].(bool)
		status = string(model.DeclaredStatus(active))
	}
	rep := map[string]string{"status": status}
	switch id.family {
	case model.FamilyTenant, model.FamilyOrganization:
		rep["slug"], rep["name"] = row.str("slug"), row.str("name")
	case model.FamilyMember:
		rep["email"], rep["tenant_role"] = row.str("email"), row.str("tenant_role")
		if name := row.str("display_name"); name != "" {
			rep["display_name"] = name
		}
	case model.FamilyOrganizationMembership:
		rep["role"] = row.str("common_role")
	}
	encoded, err := json.Marshal(rep)
	return string(encoded), err
}

// projectionOf derives the projection of a resource from its snapshot. A
// membership whose user belongs to another tenant than its organization is
// refused: the common contract never crosses tenants.
func projectionOf(db *gorm.DB, key mutationKey, raw []byte) (*ProvisioningProjection, error) {
	row, err := decodeSnapshot(raw)
	if err != nil {
		return nil, err
	}
	id, ok := projectionIdentity(key, row)
	if !ok {
		return nil, nil
	}
	if id.tenantID == "" {
		return nil, errors.WithStack(port.ErrParentNotFound)
	}
	if id.family == model.FamilyOrganizationMembership {
		var users int64
		if err := db.Table("users").Where("id = ? AND tenant_id = ?", id.key, id.tenantID).Count(&users).Error; err != nil {
			return nil, err
		}
		if users != 1 {
			return nil, errors.WithStack(port.ErrParentNotFound)
		}
	}
	rep, err := projectionRepresentation(id, row)
	if err != nil {
		return nil, err
	}
	ref, err := json.Marshal(id.reference())
	if err != nil {
		return nil, err
	}
	return &ProvisioningProjection{Family: id.family, TenantID: id.tenantID, OrgID: id.orgID, ResourceKey: id.key, Reference: string(ref), Representation: rep}, nil
}

func (p ProvisioningProjection) id() projectionID {
	return projectionID{p.Family, p.TenantID, p.OrgID, p.ResourceKey}
}

func (p ProvisioningProjection) item() (model.CommonItem, error) {
	out := model.CommonItem{Representation: json.RawMessage(p.Representation), ETag: model.CommonETag(p.Revision)}
	err := json.Unmarshal([]byte(p.Reference), &out.Key)
	return out, err
}

const projectionWhere = "family = ? AND tenant_id = ? AND org_id = ? AND resource_key = ?"

func storedProjection(db *gorm.DB, id projectionID) (*ProvisioningProjection, error) {
	var rows []ProvisioningProjection
	if err := db.Where(projectionWhere, id.family, id.tenantID, id.orgID, id.key).
		Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// projectionChange is one projection to publish: want is nil for a deletion.
type projectionChange struct {
	id     projectionID
	want   *ProvisioningProjection
	stored *ProvisioningProjection
}

// pendingProjections compares the current state of the tracked resources with
// their stored projections. It reads the resources again, so a call made under
// the feed lock sees every commit published before it.
func pendingProjections(db *gorm.DB, before map[mutationKey][]byte, keys []mutationKey) ([]projectionChange, error) {
	wants := map[projectionID]*ProvisioningProjection{}
	for _, key := range keys {
		old, err := decodeSnapshot(before[key])
		if err != nil {
			return nil, err
		}
		oldID, hadOld := projectionIdentity(key, old)
		if hadOld {
			if _, seen := wants[oldID]; !seen {
				wants[oldID] = nil
			}
		}
		raw, err := mutationSnapshot(db, key)
		if err != nil {
			return nil, err
		}
		// Unlike the backfill, which skips such a row so that broken data
		// never blocks a startup, a write to a resource crossing tenants
		// fails closed: publishing it would expose a cross-tenant fact, and
		// skipping it would leave its stored projection stale. Removing the
		// resource still works, since a deletion has no projection to derive.
		want, err := projectionOf(db, key, raw)
		if err != nil {
			return nil, err
		}
		if want == nil {
			continue
		}
		// A resource that moved to another parent leaves its former
		// projection: the old identity stays scheduled for deletion.
		wants[want.id()] = want
	}
	changes := make([]projectionChange, 0, len(wants))
	for id, want := range wants {
		stored, err := storedProjection(db, id)
		if err != nil {
			return nil, err
		}
		if want == nil && stored == nil {
			continue
		}
		if want != nil && stored != nil && want.Representation == stored.Representation {
			continue
		}
		changes = append(changes, projectionChange{id: id, want: want, stored: stored})
	}
	// Deletions first, from children to parents, then upserts from parents
	// to children.
	sort.Slice(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		if (a.want == nil) != (b.want == nil) {
			return a.want == nil
		}
		if a.want == nil {
			return b.id.less(a.id)
		}
		return a.id.less(b.id)
	})
	return changes, nil
}

// publishProjections publishes the projections of the tracked resources. A
// transaction that changes no projection never takes the feed lock.
func publishProjections(ctx context.Context, db *gorm.DB, before map[mutationKey][]byte, keys []mutationKey) error {
	changes, err := pendingProjections(db, before, keys)
	if err != nil || len(changes) == 0 {
		return err
	}
	if err := lockProvisioningFeed(db); err != nil {
		return err
	}
	// Again under the lock: at read committed (web UI, sign-in, invitations)
	// another writer may have committed a change of the same projection in
	// between. Serializable provisioning transactions only pay a re-read.
	if changes, err = pendingProjections(db, before, keys); err != nil {
		return err
	}
	var feed ProvisioningFeed
	if err := db.First(&feed, provisioningFeedID).Error; err != nil {
		return errors.WithStack(err)
	}
	actor := model.ActorFromContext(ctx)
	now := db.NowFunc().UTC()
	for _, change := range changes {
		sequence, err := nextProvisioningSequence(db)
		if err != nil {
			return err
		}
		data := model.CommonEventData{ResourceType: change.id.family, Key: change.id.reference()}
		var kind string
		switch {
		case change.want == nil:
			kind = model.CommonEventDeleted
			if err := db.Where(projectionWhere, change.id.family, change.id.tenantID, change.id.orgID, change.id.key).
				Delete(&ProvisioningProjection{}).Error; err != nil {
				return errors.WithStack(err)
			}
		default:
			kind = model.CommonEventUpdated
			if change.stored == nil {
				kind = model.CommonEventCreated
			}
			change.want.Revision = sequence
			if err := db.Clauses(clause.OnConflict{UpdateAll: true}).Create(change.want).Error; err != nil {
				return errors.WithStack(err)
			}
			data.ETag = model.CommonETag(sequence)
		}
		payload, err := json.Marshal(model.CommonEvent{
			SpecVersion: "1.0", ID: uuid.NewString(), Source: feed.Source,
			Type: model.CommonEventType(change.id.family, kind), Time: now,
			DataContentType: "application/json", Sequence: strconv.FormatInt(sequence, 10),
			RequestID: actor.RequestID, Data: data,
		})
		if err != nil {
			return err
		}
		event := ProvisioningEvent{Sequence: sequence, CreatedAt: now, TenantID: change.id.tenantID, OrgID: change.id.orgID, Family: change.id.family, ResourceKey: change.id.key, Payload: string(payload)}
		if err := db.Create(&event).Error; err != nil {
			return errors.WithStack(err)
		}
	}
	return nil
}

// lockProvisioningFeed orders the publication of this transaction after every
// publication already committed. On SQLite the statement takes the single
// writer lock, held until commit.
func lockProvisioningFeed(db *gorm.DB) error {
	if isPostgres(db) {
		return errors.WithStack(db.Exec("SELECT pg_advisory_xact_lock(?)", provisioningFeedLock).Error)
	}
	return errors.WithStack(db.Exec("UPDATE provisioning_feeds SET head = head WHERE id = ?", provisioningFeedID).Error)
}

// nextProvisioningSequence allocates a feed position. Both backends guarantee
// strictly increasing committed positions, which is all readers, ETags and
// cursors rely on: an uncommitted position is never visible nor returned.
// What happens to the position of a rolled back transaction differs:
//   - PostgreSQL takes it from a sequence, which is not transactional: the
//     position is lost and leaves a gap. A counter row would make concurrent
//     serializable publishers fail on each other.
//   - SQLite increments a counter row within the transaction: the rollback
//     undoes the increment, and the next transaction may get the same position.
//
// Never assume an allocated position stays unused after a rollback.
func nextProvisioningSequence(db *gorm.DB) (int64, error) {
	var sequence int64
	query := "UPDATE provisioning_feeds SET head = head + 1 WHERE id = 1 RETURNING head"
	if isPostgres(db) {
		query = "SELECT nextval('" + provisioningEventSequence + "')"
	}
	if err := db.Raw(query).Scan(&sequence).Error; err != nil {
		return 0, errors.WithStack(err)
	}
	if sequence <= 0 {
		return 0, errors.New("provisioning feed is not initialized")
	}
	return sequence, nil
}
