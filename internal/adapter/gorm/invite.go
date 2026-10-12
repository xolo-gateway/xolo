package gorm

import (
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type InviteToken struct {
	ID              string `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt       time.Time
	OrgID           string  `gorm:"index;not null"`
	Role            string  `gorm:"not null"`
	InviteeEmail    *string `gorm:"index"`
	ExpiresAt       *time.Time
	MaxUses         *int
	UsesCount       int    `gorm:"default:0"`
	CreatedByUserID string `gorm:"not null"`
	RevokedAt       *time.Time
	// TeamID is the team the invitee joins with the organization.
	TeamID string `gorm:"index"`

	Org *Organization `gorm:"foreignKey:OrgID"`
}

type wrappedInviteToken struct {
	t *InviteToken
}

func (w *wrappedInviteToken) ID() model.InviteTokenID       { return model.InviteTokenID(w.t.ID) }
func (w *wrappedInviteToken) OrgID() model.OrgID            { return model.OrgID(w.t.OrgID) }
func (w *wrappedInviteToken) Role() string                  { return w.t.Role }
func (w *wrappedInviteToken) InviteeEmail() *string         { return w.t.InviteeEmail }
func (w *wrappedInviteToken) ExpiresAt() *time.Time         { return w.t.ExpiresAt }
func (w *wrappedInviteToken) MaxUses() *int                 { return w.t.MaxUses }
func (w *wrappedInviteToken) UsesCount() int                { return w.t.UsesCount }
func (w *wrappedInviteToken) CreatedByUserID() model.UserID { return model.UserID(w.t.CreatedByUserID) }
func (w *wrappedInviteToken) RevokedAt() *time.Time         { return w.t.RevokedAt }
func (w *wrappedInviteToken) CreatedAt() time.Time          { return w.t.CreatedAt }
func (w *wrappedInviteToken) TeamID() model.TeamID          { return model.TeamID(w.t.TeamID) }
func (w *wrappedInviteToken) Org() model.Organization {
	if w.t.Org == nil {
		return nil
	}
	return &wrappedOrganization{w.t.Org}
}

var _ model.InviteToken = &wrappedInviteToken{}

func fromInviteToken(t model.InviteToken) *InviteToken {
	// Stored normalized whatever the implementation: ListPendingInvitesForEmail
	// relies on it for its plain equality.
	email := t.InviteeEmail()
	if email != nil {
		normalized := model.NormalizeEmail(*email)
		email = &normalized
	}
	return &InviteToken{
		ID:              string(t.ID()),
		OrgID:           string(t.OrgID()),
		Role:            t.Role(),
		InviteeEmail:    email,
		ExpiresAt:       t.ExpiresAt(),
		MaxUses:         t.MaxUses(),
		UsesCount:       t.UsesCount(),
		CreatedByUserID: string(t.CreatedByUserID()),
		RevokedAt:       t.RevokedAt(),
		TeamID:          string(t.TeamID()),
	}
}
