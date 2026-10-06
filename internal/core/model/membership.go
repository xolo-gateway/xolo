package model

import (
	"time"

	"github.com/rs/xid"
)

type MembershipID string

func NewMembershipID() MembershipID {
	return MembershipID(xid.New().String())
}

// Legacy organization role identifiers. Kept for invitation role values and
// for mapping to the corresponding builtin roles during membership creation.
const (
	RoleOrgOwner = "org:owner"
	RoleOrgAdmin = "org:admin"
	RoleMember   = "member"
)

type Membership interface {
	WithID[MembershipID]

	UserID() UserID
	OrgID() OrgID
	CreatedAt() time.Time

	// Populated via preload
	User() User
	Org() Organization
	Roles() []Role
}

type BaseMembership struct {
	id        MembershipID
	userID    UserID
	orgID     OrgID
	createdAt time.Time
	user      User
	org       Organization
	roles     []Role
}

func (m *BaseMembership) ID() MembershipID   { return m.id }
func (m *BaseMembership) UserID() UserID      { return m.userID }
func (m *BaseMembership) OrgID() OrgID        { return m.orgID }
func (m *BaseMembership) CreatedAt() time.Time { return m.createdAt }
func (m *BaseMembership) User() User          { return m.user }
func (m *BaseMembership) Org() Organization   { return m.org }
func (m *BaseMembership) Roles() []Role       { return m.roles }

var _ Membership = &BaseMembership{}

func NewMembership(userID UserID, orgID OrgID) *BaseMembership {
	return &BaseMembership{
		id:        NewMembershipID(),
		userID:    userID,
		orgID:     orgID,
		createdAt: time.Now(),
	}
}
