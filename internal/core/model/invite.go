package model

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"time"
)

type InviteTokenID string

func NewInviteTokenID() InviteTokenID {
	var token [32]byte
	// crypto/rand.Read fills the buffer and fails closed if entropy is unavailable.
	if _, err := rand.Read(token[:]); err != nil {
		panic(err)
	}
	return InviteTokenID(base64.RawURLEncoding.EncodeToString(token[:]))
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
	// TeamID is the team the invitee joins with the organization, empty for
	// none.
	TeamID() TeamID

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
	teamID          TeamID
	org             Organization
}

func (t *BaseInviteToken) ID() InviteTokenID       { return t.id }
func (t *BaseInviteToken) OrgID() OrgID            { return t.orgID }
func (t *BaseInviteToken) Role() string            { return t.role }
func (t *BaseInviteToken) InviteeEmail() *string   { return t.inviteeEmail }
func (t *BaseInviteToken) ExpiresAt() *time.Time   { return t.expiresAt }
func (t *BaseInviteToken) MaxUses() *int           { return t.maxUses }
func (t *BaseInviteToken) UsesCount() int          { return t.usesCount }
func (t *BaseInviteToken) CreatedByUserID() UserID { return t.createdByUserID }
func (t *BaseInviteToken) RevokedAt() *time.Time   { return t.revokedAt }
func (t *BaseInviteToken) CreatedAt() time.Time    { return t.createdAt }
func (t *BaseInviteToken) TeamID() TeamID          { return t.teamID }

// SetTeamID makes the invitee join the team with the organization.
func (t *BaseInviteToken) SetTeamID(teamID TeamID) { t.teamID = teamID }
func (t *BaseInviteToken) Org() Organization       { return t.org }

var _ InviteToken = &BaseInviteToken{}

// NormalizeEmail is the form invitee addresses are stored in, and the one both
// sides go through whenever Xolo compares an identity's address with an
// invitation or DefaultAdmins. The case an administrator types and the one an
// identity provider returns rarely agree, and a claim may carry stray whitespace.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NewInviteToken normalizes the invitee address. A blank one stays targeted and
// matches nobody: only a nil address makes an open invitation.
func NewInviteToken(orgID OrgID, role string, inviteeEmail *string, expiresAt *time.Time, maxUses *int, createdByUserID UserID) *BaseInviteToken {
	if inviteeEmail != nil {
		normalized := NormalizeEmail(*inviteeEmail)
		inviteeEmail = &normalized
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
	if t.ExpiresAt() != nil && !time.Now().Before(*t.ExpiresAt()) {
		return false
	}
	if t.MaxUses() != nil && t.UsesCount() >= *t.MaxUses() {
		return false
	}
	return true
}
