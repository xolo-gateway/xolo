package model

import (
	"strings"
	"time"

	"github.com/rs/xid"
)

type InviteTokenID string

func NewInviteTokenID() InviteTokenID {
	return InviteTokenID(xid.New().String())
}

// InviteToken allows new users to join an organization.
// If InviteeEmail is nil it is an open link (anyone can use it).
// If InviteeEmail is set it is targeted to a specific user.
type InviteToken interface {
	WithID[InviteTokenID]

	OrgID() OrgID
	Role() string
	InviteeEmail() *string
	ExpiresAt() *time.Time
	MaxUses() *int
	UsesCount() int
	CreatedByUserID() UserID
	RevokedAt() *time.Time
	CreatedAt() time.Time

	// Populated via preload
	Org() Organization
}

type BaseInviteToken struct {
	id              InviteTokenID
	orgID           OrgID
	role            string
	inviteeEmail    *string
	expiresAt       *time.Time
	maxUses         *int
	usesCount       int
	createdByUserID UserID
	revokedAt       *time.Time
	createdAt       time.Time
	org             Organization
}

func (t *BaseInviteToken) ID() InviteTokenID          { return t.id }
func (t *BaseInviteToken) OrgID() OrgID               { return t.orgID }
func (t *BaseInviteToken) Role() string                { return t.role }
func (t *BaseInviteToken) InviteeEmail() *string       { return t.inviteeEmail }
func (t *BaseInviteToken) ExpiresAt() *time.Time       { return t.expiresAt }
func (t *BaseInviteToken) MaxUses() *int               { return t.maxUses }
func (t *BaseInviteToken) UsesCount() int              { return t.usesCount }
func (t *BaseInviteToken) CreatedByUserID() UserID     { return t.createdByUserID }
func (t *BaseInviteToken) RevokedAt() *time.Time       { return t.revokedAt }
func (t *BaseInviteToken) CreatedAt() time.Time        { return t.createdAt }
func (t *BaseInviteToken) Org() Organization           { return t.org }

var _ InviteToken = &BaseInviteToken{}

// NormalizeEmail is the form invitee addresses are stored and looked up in. The
// case an administrator types and the one an identity provider returns rarely
// agree, and an address pasted from a mail client often drags a space along.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func NewInviteToken(orgID OrgID, role string, inviteeEmail *string, expiresAt *time.Time, maxUses *int, createdByUserID UserID) *BaseInviteToken {
	if inviteeEmail != nil {
		if normalized := NormalizeEmail(*inviteeEmail); normalized != "" {
			inviteeEmail = &normalized
		} else {
			inviteeEmail = nil
		}
	}

	return &BaseInviteToken{
		id:              NewInviteTokenID(),
		orgID:           orgID,
		role:            role,
		inviteeEmail:    inviteeEmail,
		expiresAt:       expiresAt,
		maxUses:         maxUses,
		usesCount:       0,
		createdByUserID: createdByUserID,
		createdAt:       time.Now(),
	}
}

// IsValid returns true if the invite can still be accepted.
func IsInviteValid(t InviteToken) bool {
	if t.RevokedAt() != nil {
		return false
	}
	if t.ExpiresAt() != nil && time.Now().After(*t.ExpiresAt()) {
		return false
	}
	if t.MaxUses() != nil && t.UsesCount() >= *t.MaxUses() {
		return false
	}
	return true
}
