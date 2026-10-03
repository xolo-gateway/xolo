package gorm

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"gorm.io/gorm"
)

// PublicationClock is a transactional counter, not a SQL sequence. Every
// identity writer locks this row before reading parents, then keeps the lock
// until commit. No lower sequence can become visible after a higher one.
type PublicationClock struct {
	ID       int `gorm:"primaryKey;autoIncrement:false"`
	Sequence int64
}
type MutationAudit struct {
	Sequence   int64 `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt  time.Time
	Actor      string
	RequestID  string
	Resource   string
	ResourceID string
	Before     string `gorm:"type:text"`
	After      string `gorm:"type:text"`
}

// Publication is the closed common CloudEvent outbox, independent of the local
// audit event ring buffer. Delivery is handled by the optional webhook worker.
type Publication struct {
	TenantID   string `gorm:"index"`
	OrgID      string `gorm:"index"`
	Sequence   int64  `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt  time.Time
	Resource   string
	ResourceID string
	Payload    string `gorm:"type:text"`
}
type mutationKey struct{ kind, id string }
type mutationState struct {
	authenticatedMember string
	before              map[mutationKey][]byte
	commonPUT           bool
	actor               model.Actor
	condition           *model.MatchCondition
	etag                string
}

func (s *Store) WithProvisioningTransaction(ctx context.Context, fn func(port.ProvisioningTx) error) error {
	return s.identityTransaction(ctx, func(bound *Store) error { return fn(bound) })
}
func (s *Store) identityTransaction(ctx context.Context, fn func(*Store) error) error {
	if s.mutations != nil {
		return fn(s)
	}
	actor := model.ActorFromContext(ctx)
	if actor.UserID != "" && actor.URI != "" {
		return port.ErrNotAllowed
	}
	if actor.UserID == "" && actor.URI == "" {
		actor.URI = "urn:xolo:operator:local"
	}
	decodedID, idErr := hex.DecodeString(actor.RequestID)
	if idErr != nil || len(decodedID) != 16 || strings.ToLower(actor.RequestID) != actor.RequestID {
		actor.RequestID = strings.ReplaceAll(uuid.NewString(), "-", "")
		ctx = model.WithActor(ctx, actor)
	}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// UPDATE obtains the SQLite writer lock and a PostgreSQL row lock before
			// any validation. Lock order: clock, then parents, children and facts.
			result := tx.Exec("UPDATE publication_clocks SET sequence = sequence WHERE id = 1")
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("missing publication clock")
			}
			tx = tx.Session(&gorm.Session{SkipDefaultTransaction: true})
			bound := &Store{lifecycleRetention: s.lifecycleRetention, businessEnabled: s.businessEnabled, businessSecretKey: s.businessSecretKey, identityProviders: s.identityProviders, ownership: s.ownership, getDatabase: func(context.Context) (*gorm.DB, error) { return tx, nil }, transactionBound: true, mutations: &mutationState{before: map[mutationKey][]byte{}, actor: actor, commonPUT: model.IsCommonPUT(ctx)}}
			if err := fn(bound); err != nil {
				return err
			}
			return bound.flushMutations(ctx, tx)
		})
		if err == nil || !isRetryableError(err) || attempt >= 10 {
			return lifecycleError(err)
		}
		timer := time.NewTimer(min(10*time.Millisecond<<attempt, 500*time.Millisecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// mutate tracks only the affected resource. Nested operations coalesce to one
// before/after pair and facts are written only after the whole use case succeeds.
func (s *Store) mutate(ctx context.Context, kind, id string, fn func(*Store) error) error {
	return s.identityTransaction(ctx, func(bound *Store) error {
		if c := bound.mutations.condition; c != nil && !c.Matches(bound.mutations.etag) {
			return port.ErrPreconditionFailed
		}
		if err := bound.checkOwnership(ctx, kind); err != nil {
			return err
		}
		if err := bound.track(ctx, kind, id); err != nil {
			return err
		}
		return fn(bound)
	})
}
func (s *Store) track(ctx context.Context, kind, id string) error {
	key := mutationKey{kind, id}
	if _, ok := s.mutations.before[key]; ok {
		return nil
	}
	db, err := s.getDatabase(ctx)
	if err != nil {
		return err
	}
	before, err := mutationSnapshot(db, key)
	if err != nil {
		return err
	}
	s.mutations.before[key] = before
	return nil
}

var resourceTables = map[string]string{"tenant": "tenants", "organization": "organizations", "member": "users", "membership": "memberships", "role": "roles", "domain": "domains", "invitation": "invite_tokens", "application": "applications", "quota": "quota", "alert": "alerts", "provider": "providers"}

func mutationSnapshot(db *gorm.DB, key mutationKey) ([]byte, error) {
	table, ok := resourceTables[key.kind]
	if !ok {
		return nil, fmt.Errorf("unknown mutation kind")
	}
	column := "id"
	if key.kind == "domain" {
		column = "hostname"
	}
	var rows []map[string]any
	if err := db.Table(table).Where(column+" = ?", key.id).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []byte("null"), nil
	}
	row := rows[0]
	delete(row, "created_at")
	delete(row, "updated_at")
	// Normalize driver values so byte/text representations cannot create facts.
	for k, v := range row {
		if b, ok := v.([]byte); ok {
			row[k] = string(b)
		}
	}
	if key.kind == "membership" || key.kind == "role" || key.kind == "invitation" {
		var tenantID string
		if err := db.Table("organizations").Where("id = ?", row["org_id"]).Pluck("tenant_id", &tenantID).Error; err != nil {
			return nil, err
		}
		row["tenant_id"] = tenantID
	}
	if key.kind == "membership" {
		var roles []string
		if err := db.Table("membership_roles").Where("membership_id = ?", key.id).Order("role_id").Pluck("role_id", &roles).Error; err != nil {
			return nil, err
		}
		row["roles"] = roles
	}
	if key.kind == "member" {
		row["active"] = fmt.Sprint(row["active"]) == "true" || fmt.Sprint(row["active"]) == "1"
		var roles []string
		if err := db.Table("user_roles").Where("user_id = ?", key.id).Order("role").Pluck("role", &roles).Error; err != nil {
			return nil, err
		}
		row["platform_roles"] = roles
	}
	if key.kind == "role" {
		var permissions []string
		if err := db.Table("role_permissions").Where("role_id = ?", key.id).Order("code").Pluck("code", &permissions).Error; err != nil {
			return nil, err
		}
		row["permissions"] = permissions
		var grants []map[string]any
		if err := db.Table("role_models").Select("model_id, model_kind").Where("role_id = ?", key.id).Order("model_id, model_kind").Find(&grants).Error; err != nil {
			return nil, err
		}
		row["grants"] = grants
	}
	if err := businessSnapshot(db, key, row); err != nil {
		return nil, err
	}
	return json.Marshal(row)
}
func (s *Store) flushMutations(ctx context.Context, db *gorm.DB) error {
	keys := make([]mutationKey, 0, len(s.mutations.before))
	for k := range s.mutations.before {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].id < keys[j].id
	})
	actor := s.mutations.actor
	ctx = model.WithActor(ctx, actor)
	actorJSON, err := json.Marshal(actor)
	if err != nil {
		return err
	}
	for _, key := range keys {
		before := s.mutations.before[key]
		after, err := mutationSnapshot(db, key)
		if err != nil {
			return err
		}
		if bytes.Equal(before, after) {
			continue
		}
		// Cascades tracked through parent deletion must obey every family's
		// ownership as well. Login has a narrow exception for its proven account.
		if key.kind != "member" || key.id != s.mutations.authenticatedMember {
			if err := s.checkOwnership(ctx, key.kind); err != nil {
				return err
			}
		}
		if err := validateMutationScope(db, key, before, after); err != nil {
			return err
		}
		if err := checkOwnerTransition(db, key, before, after); err != nil {
			return err
		}
		if err := db.Exec("UPDATE publication_clocks SET sequence = sequence + 1 WHERE id = 1").Error; err != nil {
			return err
		}
		var clock PublicationClock
		if err := db.First(&clock, 1).Error; err != nil {
			return err
		}
		audit := MutationAudit{Sequence: clock.Sequence, Actor: string(actorJSON), RequestID: actor.RequestID, Resource: key.kind, ResourceID: key.id, Before: string(before), After: string(after)}
		if err := recordMutationEvent(ctx, db, key, before, after); err != nil {
			return err
		}
		if err := db.Create(&audit).Error; err != nil {
			return err
		}
		if err := publishCommon(ctx, db, key, before, after, s.mutations.commonPUT, s.businessEnabled); err != nil {
			return err
		}
	}
	return nil
}
func checkOwnerTransition(db *gorm.DB, key mutationKey, before, after []byte) error {
	var old, new map[string]any
	if err := json.Unmarshal(before, &old); err != nil {
		return err
	}
	if err := json.Unmarshal(after, &new); err != nil {
		return err
	}
	var count int64
	switch key.kind {
	case "member":
		if old["tenant_role"] != "owner" || old["active"] != true || (new["tenant_role"] == "owner" && new["active"] == true) {
			return nil
		}
		// Whole-scope deletion is a separate lifecycle operation.
		var parent int64
		if err := db.Table("tenants").Where("id = ?", old["tenant_id"]).Count(&parent).Error; err != nil {
			return err
		}
		if parent == 0 {
			return nil
		}
		if err := db.Table("users").Where("tenant_id = ? AND tenant_role = ? AND active = ?", old["tenant_id"], "owner", true).Count(&count).Error; err != nil {
			return err
		}
	case "membership":
		if old["common_role"] != "owner" || old["status"] != "active" || (new["common_role"] == "owner" && new["status"] == "active") {
			return nil
		}
		var parent int64
		if err := db.Table("organizations").Where("id = ?", old["org_id"]).Count(&parent).Error; err != nil {
			return err
		}
		if parent == 0 {
			return nil
		}
		if err := db.Table("memberships").Where("org_id = ? AND common_role = ? AND status = ?", old["org_id"], "owner", "active").Count(&count).Error; err != nil {
			return err
		}
	default:
		return nil
	}
	if count == 0 {
		return port.ErrLastOwner
	}
	return nil
}

// Local facts keep their existing event types and remain separate from the
// synchronization outbox. Alert evaluation consumes these committed rows.
func recordMutationEvent(ctx context.Context, db *gorm.DB, key mutationKey, before, after []byte) error {
	var old, current map[string]any
	if err := json.Unmarshal(before, &old); err != nil {
		return err
	}
	if err := json.Unmarshal(after, &current); err != nil {
		return err
	}
	row := current
	if row == nil {
		row = old
	}
	if row == nil {
		return nil
	}
	str := func(key string) string { v, _ := row[key].(string); return v }
	typ := ""
	attrs := map[string]string{}
	switch key.kind {
	case "membership":
		typ = model.EventTypeMemberUpdated
		if old == nil {
			typ = model.EventTypeMemberAdded
		}
		if current == nil {
			typ = model.EventTypeMemberRemoved
		}
		attrs["membership_id"] = key.id
		attrs["member_user_id"] = str("user_id")
		if roles, ok := row["roles"].([]any); ok {
			attrs["role_count"] = strconv.Itoa(len(roles))
		}
	case "role":
		if row["builtin"] == true || row["builtin"] == float64(1) {
			return nil
		}
		typ = model.EventTypeRoleUpdated
		if old == nil {
			typ = model.EventTypeRoleCreated
		}
		if current == nil {
			typ = model.EventTypeRoleDeleted
		}
		attrs["role_id"] = key.id
		attrs["role_name"] = str("name")
	case "invitation":
		if old != nil && current != nil {
			return nil
		}
		typ = model.EventTypeInviteCreated
		if current == nil {
			typ = model.EventTypeInviteDeleted
		}
		attrs["invite_id"] = key.id
		attrs["role"] = str("role")
		if email := str("invitee_email"); email != "" {
			attrs["email"] = email
		}
	default:
		return nil
	}
	if orgID := str("org_id"); orgID != "" {
		var count int64
		if err := db.Table("organizations").Where("id = ?", orgID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
	}
	actor := model.ActorFromContext(ctx)
	attrs["actor_id"] = string(actor.UserID)
	attrs["actor_uri"] = actor.URI
	attrs["request_id"] = actor.RequestID
	attrs["actor"] = actor.URI
	if actor.UserID != "" {
		var user User
		if err := db.Select("display_name").Where("id = ?", string(actor.UserID)).Find(&user).Error; err != nil {
			return err
		}
		attrs["actor"] = user.DisplayName
	}
	messages := map[string]string{
		model.EventTypeMemberAdded:   "Membre ajouté à l'organisation",
		model.EventTypeMemberUpdated: "Adhésion modifiée",
		model.EventTypeMemberRemoved: "Membre retiré de l'organisation",
		model.EventTypeRoleCreated:   "Rôle créé : " + str("name"),
		model.EventTypeRoleUpdated:   "Rôle modifié : " + str("name"),
		model.EventTypeRoleDeleted:   "Rôle supprimé : " + str("name"),
		model.EventTypeInviteCreated: "Invitation créée",
		model.EventTypeInviteDeleted: "Invitation supprimée",
	}
	severity := model.SeverityInfo
	if current == nil {
		severity = model.SeverityWarning
	}
	event := model.NewEvent(model.EventSourcePlatform, typ, model.WithEventOrg(model.OrgID(str("org_id"))), model.WithEventAttributes(attrs), model.WithEventMessage(messages[typ]), model.WithEventSeverity(severity))
	if err := db.Create(fromEvent(event)).Error; err != nil {
		return err
	}
	// Preserve the separate local fact for initial role assignment; the common
	// resource still has only one before/after publication for this commit.
	if key.kind == "membership" && old == nil {
		if roles, ok := row["roles"].([]any); ok && len(roles) > 0 {
			assigned := model.NewEvent(model.EventSourcePlatform, model.EventTypeMemberUpdated, model.WithEventOrg(model.OrgID(str("org_id"))), model.WithEventAttributes(attrs), model.WithEventMessage("Rôles de membre attribués"))
			return db.Create(fromEvent(assigned)).Error
		}
	}
	return nil
}
