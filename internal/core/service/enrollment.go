package service

import (
	"context"
	stderrors "errors"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

const (
	// enrollmentRulesTTL bounds how long another replica keeps serving rules
	// that changed: a tenant reads its rules once per period, however many
	// requests it receives.
	enrollmentRulesTTL  = 30 * time.Second
	enrollmentCacheSize = 4096
	// Every request of a bearer token comes through here: what is settled
	// is remembered so that it costs no query.
	enrollmentSettledSize = 65536
	enrollmentSettledTTL  = time.Hour
	// enrollmentRetryDelay spaces the attempts of a rule that fails.
	enrollmentRetryDelay = time.Minute
	// enrollmentActivationWindow bounds how long a rule that failed on the
	// sign-in that created an account may still activate it.
	enrollmentActivationWindow = time.Hour
)

// Enrollment is a membership a rule created.
type Enrollment struct {
	Rule       model.EnrollmentRule
	Membership model.MembershipID
	Role       model.Role
	// Activated tells that the rule activated the account.
	Activated bool
}

// EnrollmentService keeps the enrollment rules and applies them to the
// identities that sign in. A rule acts once per account: what an administrator
// changes afterwards, removing a member or deactivating an account, prevails.
type EnrollmentService struct {
	store        port.EnrollmentStore
	transactions port.ProvisioningTransaction
	rules        *expirable.LRU[model.TenantID, *tenantRules]
	settled      *expirable.LRU[string, time.Time]
}

// tenantRules are the active rules of a tenant, and the claim values they
// compare.
type tenantRules struct {
	rules  []model.EnrollmentRule
	claims map[string][]string
}

func NewEnrollmentService(store port.EnrollmentStore, transactions port.ProvisioningTransaction) *EnrollmentService {
	return &EnrollmentService{
		store:        store,
		transactions: transactions,
		rules:        expirable.NewLRU[model.TenantID, *tenantRules](enrollmentCacheSize, nil, enrollmentRulesTTL),
		settled:      expirable.NewLRU[string, time.Time](enrollmentSettledSize, nil, enrollmentSettledTTL),
	}
}

// ValidateEnrollmentRule checks what a rule holds on its own. Its role is
// checked against its organization by checkEnrollmentRole.
func ValidateEnrollmentRule(rule model.EnrollmentRule) error {
	if strings.TrimSpace(rule.Name()) == "" {
		return errors.Wrap(port.ErrInvalid, "an enrollment rule needs a name")
	}
	switch rule.Kind() {
	case model.EnrollmentKindEmailDomain:
		domain := model.NormalizeEnrollmentDomain(rule.Value())
		if rule.Claim() != "" || !strings.Contains(domain, ".") || strings.ContainsAny(domain, "@ \t") {
			return errors.Wrap(port.ErrInvalid, "invalid email domain")
		}
	case model.EnrollmentKindClaim:
		if rule.Claim() == "" || rule.Value() == "" {
			return errors.Wrap(port.ErrInvalid, "a claim rule needs a claim and a value")
		}
	default:
		return errors.Wrap(port.ErrInvalid, "unknown enrollment rule kind")
	}
	if rule.Role() == "" {
		return errors.Wrap(port.ErrInvalid, "an enrollment rule needs a role")
	}
	return nil
}

// checkEnrollmentRole refuses a role outside the organization and the owner
// role: a rule never hands out the organization.
func checkEnrollmentRole(ctx context.Context, tx orgRoleReader, rule model.EnrollmentRule) (model.Role, error) {
	role, err := resolveOrgRole(ctx, tx, rule.OrgID(), rule.Role())
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if role.Builtin() && role.BuiltinKind() == model.BuiltinKindOwner {
		return nil, errors.Wrap(port.ErrInvalid, "an enrollment rule can not grant the owner role")
	}
	return role, nil
}

// normalizedEnrollmentRule stores a domain in its normalized form, and a claim
// rule without the spaces around its claim and value, which no claim of a
// provider would match.
func normalizedEnrollmentRule(rule model.EnrollmentRule) model.EnrollmentRule {
	switch rule.Kind() {
	case model.EnrollmentKindEmailDomain:
		return model.UpdateEnrollmentRule(rule,
			model.WithEnrollmentRuleClaim(strings.TrimSpace(rule.Claim())),
			model.WithEnrollmentRuleValue(model.NormalizeEnrollmentDomain(rule.Value())),
		)
	case model.EnrollmentKindClaim:
		return model.UpdateEnrollmentRule(rule,
			model.WithEnrollmentRuleClaim(strings.TrimSpace(rule.Claim())),
			model.WithEnrollmentRuleValue(strings.TrimSpace(rule.Value())),
		)
	}
	return rule
}

// ListRules returns the rules of an organization.
func (s *EnrollmentService) ListRules(ctx context.Context, orgID model.OrgID) ([]model.EnrollmentRule, error) {
	rules, err := s.store.ListEnrollmentRules(ctx, orgID)
	return rules, errors.WithStack(err)
}

// GetRule returns a rule of an organization. A rule of another organization
// is reported as not found.
func (s *EnrollmentService) GetRule(ctx context.Context, orgID model.OrgID, id model.EnrollmentRuleID) (model.EnrollmentRule, error) {
	rule, err := s.store.GetEnrollmentRuleByID(ctx, id)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if rule.OrgID() != orgID {
		return nil, errors.WithStack(port.ErrNotFound)
	}
	return rule, nil
}

// SaveRule creates the rule, or updates the one it replaces.
func (s *EnrollmentService) SaveRule(ctx context.Context, tenantID model.TenantID, rule model.EnrollmentRule, create bool) error {
	normalized := normalizedEnrollmentRule(rule)
	if err := ValidateEnrollmentRule(normalized); err != nil {
		return err
	}
	err := s.transactions.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		org, err := tx.GetOrgByID(ctx, rule.OrgID())
		if err != nil {
			return errors.WithStack(err)
		}
		if org.TenantID() != tenantID {
			return errors.WithStack(port.ErrNotFound)
		}
		if _, err := checkEnrollmentRole(ctx, tx, rule); err != nil {
			return err
		}
		if create {
			return errors.WithStack(tx.CreateEnrollmentRule(ctx, normalized))
		}
		existing, err := tx.GetEnrollmentRuleByID(ctx, rule.ID())
		if err != nil {
			return errors.WithStack(err)
		}
		if existing.OrgID() != rule.OrgID() {
			return errors.WithStack(port.ErrNotFound)
		}
		return errors.WithStack(tx.UpdateEnrollmentRule(ctx, normalized))
	})
	if err != nil {
		return err
	}
	s.rules.Remove(tenantID)
	return nil
}

// DeleteRule removes a rule of an organization. Memberships stay, and so does
// the record of the accounts it acted on: a member an administrator removed
// is not enrolled again by a rule created afterwards.
func (s *EnrollmentService) DeleteRule(ctx context.Context, tenantID model.TenantID, orgID model.OrgID, id model.EnrollmentRuleID) error {
	if _, err := s.GetRule(ctx, orgID, id); err != nil {
		return err
	}
	if err := s.store.DeleteEnrollmentRule(ctx, id); err != nil {
		return errors.WithStack(err)
	}
	s.rules.Remove(tenantID)
	return nil
}

// Enroll applies to the account the rules its identity matches that did not
// act on it yet. A rule that fails does not stop the others: the error comes
// with the enrollments that succeeded.
func (s *EnrollmentService) Enroll(ctx context.Context, tenantID model.TenantID, user model.User, identity model.EnrollmentIdentity) ([]Enrollment, error) {
	active, err := s.activeRules(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var pending []model.EnrollmentRule
	for _, rule := range active.rules {
		if model.MatchesEnrollmentRule(rule, identity) && !s.isSettled(user, rule) {
			pending = append(pending, rule)
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}
	applied, err := s.store.ListAppliedEnrollmentRules(ctx, user.ID())
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var (
		enrollments []Enrollment
		failures    []error
	)
	// Only an account still inactive is activated. That its sign-in created
	// it is stored, not kept in memory: the retry of a rule that fails may
	// reach another replica. The first rule that activates the account
	// clears the mark, so a later deactivation prevails.
	activate := false
	switch {
	case user.Active():
	case identity.NewAccount:
		activate = true
		if err := s.store.MarkEnrollmentActivationPending(ctx, user.ID()); err != nil {
			failures = append(failures, errors.Wrap(err, "remember the account to activate"))
		}
	default:
		activate, err = s.store.IsEnrollmentActivationPending(ctx, user.ID(), time.Now().Add(-enrollmentActivationWindow))
		if err != nil {
			return nil, errors.WithStack(err)
		}
	}
	for _, rule := range pending {
		// A concurrent request that did not know the account was new may have
		// applied the rule: the one that created it still activates it.
		if slices.Contains(applied, rule.ID()) && !activate {
			s.settle(user, rule, enrollmentSettledTTL)
			continue
		}
		enrollment, outcome, err := s.apply(ctx, user, rule, activate)
		if err != nil {
			s.settle(user, rule, enrollmentRetryDelay)
			failures = append(failures, errors.Wrapf(err, "enrollment rule %s", rule.ID()))
			continue
		}
		if outcome == ruleWithdrawn {
			// The cached rules are stale: the next request reads them again,
			// and acts on the rule if it is enabled again by then.
			s.rules.Remove(tenantID)
			continue
		}
		s.settle(user, rule, enrollmentSettledTTL)
		if enrollment != nil {
			enrollments = append(enrollments, *enrollment)
		}
	}
	// No rule is left to retry: nothing will activate the account anymore.
	if activate && len(failures) == 0 {
		if err := s.store.ClearEnrollmentActivationPending(ctx, user.ID()); err != nil {
			failures = append(failures, errors.WithStack(err))
		}
	}
	return enrollments, stderrors.Join(failures...)
}

// ComparedClaims returns, per claim name, the values the active rules of a
// tenant compare. The caller must not modify it.
func (s *EnrollmentService) ComparedClaims(ctx context.Context, tenantID model.TenantID) (map[string][]string, error) {
	active, err := s.activeRules(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return active.claims, nil
}

func (s *EnrollmentService) activeRules(ctx context.Context, tenantID model.TenantID) (*tenantRules, error) {
	if active, ok := s.rules.Get(tenantID); ok {
		return active, nil
	}
	rules, err := s.store.ListActiveEnrollmentRules(ctx, tenantID)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	active := &tenantRules{rules: rules, claims: map[string][]string{}}
	for _, rule := range rules {
		if rule.Kind() == model.EnrollmentKindClaim && !slices.Contains(active.claims[rule.Claim()], rule.Value()) {
			active.claims[rule.Claim()] = append(active.claims[rule.Claim()], rule.Value())
		}
	}
	s.rules.Add(tenantID, active)
	return active, nil
}

func settledKey(user model.User, rule model.EnrollmentRule) string {
	return string(user.ID()) + ":" + string(rule.ID())
}

func (s *EnrollmentService) isSettled(user model.User, rule model.EnrollmentRule) bool {
	until, ok := s.settled.Get(settledKey(user, rule))
	return ok && time.Now().Before(until)
}

func (s *EnrollmentService) settle(user model.User, rule model.EnrollmentRule, duration time.Duration) {
	s.settled.Add(settledKey(user, rule), time.Now().Add(duration))
}

// applyOutcome tells whether a rule still applies.
type applyOutcome int

const (
	ruleSettled applyOutcome = iota
	// ruleWithdrawn reports a rule deleted, disabled or whose organization
	// was deactivated since the rules were read.
	ruleWithdrawn
)

// apply runs one rule in its own transaction, which replays everything the
// read decided: the rule may have changed, and a concurrent request may have
// applied it. The application is recorded first, so that the concurrent
// request waits for this one. It returns nil when no membership was created.
// activate allows activating the account, for one this sign-in created.
func (s *EnrollmentService) apply(ctx context.Context, user model.User, rule model.EnrollmentRule, activate bool) (*Enrollment, applyOutcome, error) {
	actor := model.ActorFromContext(model.EnsureActor(ctx))
	ctx = model.WithActor(ctx, model.Actor{UserID: user.ID(), RequestID: actor.RequestID})
	// The rule may activate the account of the identity signing in, as a
	// sign-in writes its own account.
	ctx = model.WithSignInAccount(ctx, user.ID())

	var (
		result  *Enrollment
		outcome applyOutcome
	)
	err := s.transactions.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		result, outcome = nil, ruleSettled
		current, err := tx.GetEnrollmentRuleByID(ctx, rule.ID())
		if errors.Is(err, port.ErrNotFound) {
			outcome = ruleWithdrawn
			return nil
		}
		if err != nil {
			return errors.WithStack(err)
		}
		org, err := tx.GetOrgByID(ctx, current.OrgID())
		if err != nil {
			return errors.WithStack(err)
		}
		if org.TenantID() != user.TenantID() {
			return nil
		}
		if !current.Enabled() || !org.Active() {
			outcome = ruleWithdrawn
			return nil
		}
		recorded, err := tx.RecordEnrollmentApplication(ctx, current, user.ID())
		if err != nil {
			return errors.WithStack(err)
		}
		if !recorded {
			if !activate {
				return nil
			}
			// The rule already enrolled the account this sign-in created.
			if _, err := tx.GetUserOrgMembership(ctx, user.ID(), org.ID()); errors.Is(err, port.ErrNotFound) {
				return nil
			} else if err != nil {
				return errors.WithStack(err)
			}
			_, err := activateAccount(ctx, tx, user.ID())
			return err
		}
		// Another rule of the organization already acted on the account: a
		// member an administrator removed stays out, whichever rule matches.
		applications, err := tx.CountEnrollmentApplications(ctx, org.ID(), user.ID())
		if err != nil {
			return errors.WithStack(err)
		}
		if applications > 1 {
			return nil
		}
		// A member keeps what an administrator gave them, a suspension
		// included.
		if _, err := tx.GetUserOrgMembership(ctx, user.ID(), org.ID()); err == nil {
			return nil
		} else if !errors.Is(err, port.ErrNotFound) {
			return errors.WithStack(err)
		}
		role, err := checkEnrollmentRole(ctx, tx, current)
		if err != nil {
			return err
		}
		membership := model.NewMembership(user.ID(), org.ID())
		if err := tx.AddMember(ctx, membership); err != nil {
			return errors.WithStack(err)
		}
		if err := tx.SetMembershipRoles(ctx, membership.ID(), []model.RoleID{role.ID()}); err != nil {
			return errors.WithStack(err)
		}
		enrollment := &Enrollment{Rule: current, Membership: membership.ID(), Role: role}
		if activate {
			if enrollment.Activated, err = activateAccount(ctx, tx, user.ID()); err != nil {
				return err
			}
		}
		result = enrollment
		return nil
	})
	if err != nil {
		return nil, ruleSettled, err
	}
	return result, outcome, nil
}

// activateAccount activates an inactive account, and tells whether it did.
// The account is no longer one to activate: what an administrator decides
// afterwards prevails.
func activateAccount(ctx context.Context, tx port.ProvisioningTx, userID model.UserID) (bool, error) {
	if err := tx.ClearEnrollmentActivationPending(ctx, userID); err != nil {
		return false, errors.WithStack(err)
	}
	account, err := tx.GetUserByID(ctx, userID)
	if err != nil {
		return false, errors.WithStack(err)
	}
	if account.Active() {
		return false, nil
	}
	next := model.CopyUser(account)
	next.SetActive(true)
	return true, errors.WithStack(tx.SaveUser(ctx, next))
}
