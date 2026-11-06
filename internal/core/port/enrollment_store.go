package port

import (
	"context"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

type EnrollmentStore interface {
	CreateEnrollmentRule(ctx context.Context, rule model.EnrollmentRule) error
	UpdateEnrollmentRule(ctx context.Context, rule model.EnrollmentRule) error
	DeleteEnrollmentRule(ctx context.Context, id model.EnrollmentRuleID) error
	GetEnrollmentRuleByID(ctx context.Context, id model.EnrollmentRuleID) (model.EnrollmentRule, error)
	ListEnrollmentRules(ctx context.Context, orgID model.OrgID) ([]model.EnrollmentRule, error)
	// ListActiveEnrollmentRules returns the enabled rules of the active
	// organizations of a tenant, oldest first, leaving out the frozen scopes.
	ListActiveEnrollmentRules(ctx context.Context, tenantID model.TenantID) ([]model.EnrollmentRule, error)
	// ListAppliedEnrollmentRules returns the rules that already acted on a
	// user.
	ListAppliedEnrollmentRules(ctx context.Context, userID model.UserID) ([]model.EnrollmentRuleID, error)
	// RecordEnrollmentApplication records that a rule acted on a user, and
	// reports false when it already had. It is what makes a rule act once.
	RecordEnrollmentApplication(ctx context.Context, rule model.EnrollmentRule, userID model.UserID) (bool, error)
	// CountEnrollmentApplications counts the rules of an organization that
	// acted on a user, deleted rules included.
	CountEnrollmentApplications(ctx context.Context, orgID model.OrgID, userID model.UserID) (int64, error)
	// MarkEnrollmentActivationPending remembers that a sign-in created the
	// account inactive and that a rule may still activate it.
	MarkEnrollmentActivationPending(ctx context.Context, userID model.UserID) error
	// IsEnrollmentActivationPending tells whether the account was marked
	// since the given time.
	IsEnrollmentActivationPending(ctx context.Context, userID model.UserID, since time.Time) (bool, error)
	ClearEnrollmentActivationPending(ctx context.Context, userID model.UserID) error
}
