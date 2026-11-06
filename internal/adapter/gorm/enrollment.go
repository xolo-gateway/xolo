package gorm

import (
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type EnrollmentRule struct {
	ID        string `gorm:"primaryKey;autoIncrement:false"`
	OrgID     string `gorm:"index"`
	Name      string
	Kind      string
	Claim     string
	Value     string
	Role      string
	Enabled   bool `gorm:"index"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EnrollmentApplication records that a rule acted on a user. It outlives the
// membership: a member an administrator removed is not enrolled again.
type EnrollmentApplication struct {
	RuleID    string `gorm:"primaryKey;autoIncrement:false"`
	UserID    string `gorm:"primaryKey;autoIncrement:false;index"`
	OrgID     string `gorm:"index"`
	CreatedAt time.Time
}

// EnrollmentPendingActivation remembers an account a sign-in created inactive
// while a rule that would activate it has not acted yet, so that any replica
// finishes the job.
type EnrollmentPendingActivation struct {
	UserID    string `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt time.Time
}

type wrappedEnrollmentRule struct {
	r *EnrollmentRule
}

func (w *wrappedEnrollmentRule) ID() model.EnrollmentRuleID { return model.EnrollmentRuleID(w.r.ID) }
func (w *wrappedEnrollmentRule) OrgID() model.OrgID         { return model.OrgID(w.r.OrgID) }
func (w *wrappedEnrollmentRule) Name() string               { return w.r.Name }
func (w *wrappedEnrollmentRule) Kind() model.EnrollmentKind { return model.EnrollmentKind(w.r.Kind) }
func (w *wrappedEnrollmentRule) Claim() string              { return w.r.Claim }
func (w *wrappedEnrollmentRule) Value() string              { return w.r.Value }
func (w *wrappedEnrollmentRule) Role() string               { return w.r.Role }
func (w *wrappedEnrollmentRule) Enabled() bool              { return w.r.Enabled }
func (w *wrappedEnrollmentRule) CreatedAt() time.Time       { return w.r.CreatedAt }
func (w *wrappedEnrollmentRule) UpdatedAt() time.Time       { return w.r.UpdatedAt }

var _ model.EnrollmentRule = &wrappedEnrollmentRule{}

func fromEnrollmentRule(r model.EnrollmentRule) *EnrollmentRule {
	return &EnrollmentRule{
		ID:        string(r.ID()),
		OrgID:     string(r.OrgID()),
		Name:      r.Name(),
		Kind:      string(r.Kind()),
		Claim:     r.Claim(),
		Value:     r.Value(),
		Role:      r.Role(),
		Enabled:   r.Enabled(),
		CreatedAt: r.CreatedAt(),
		UpdatedAt: r.UpdatedAt(),
	}
}
