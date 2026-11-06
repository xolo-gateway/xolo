package model

import (
	"slices"
	"strings"
	"time"

	"github.com/rs/xid"
)

type EnrollmentRuleID string

func NewEnrollmentRuleID() EnrollmentRuleID {
	return EnrollmentRuleID(xid.New().String())
}

// EnrollmentKind is what an enrollment rule compares.
type EnrollmentKind string

const (
	// EnrollmentKindEmailDomain matches the domain of an email the identity
	// provider asserted as verified.
	EnrollmentKindEmailDomain EnrollmentKind = "email_domain"
	// EnrollmentKindClaim matches a value of a claim of the identity
	// provider.
	EnrollmentKindClaim EnrollmentKind = "claim"
)

// Valid tells whether k is a known kind.
func (k EnrollmentKind) Valid() bool {
	return k == EnrollmentKindEmailDomain || k == EnrollmentKindClaim
}

// EnrollmentRule makes the identities it matches members of its organization,
// with its role, without any administrator action. A rule acts once per
// account: what an administrator changes afterwards prevails over it.
type EnrollmentRule interface {
	WithID[EnrollmentRuleID]
	WithLifecycle

	OrgID() OrgID
	Name() string
	Kind() EnrollmentKind
	// Claim names the compared claim of a claim rule.
	Claim() string
	// Value is the email domain or the claim value to match.
	Value() string
	// Role uses the values of an invitation: a builtin role, or the ID of a
	// custom role of the organization.
	Role() string
	Enabled() bool
}

// EnrollmentIdentity is what a rule can compare.
type EnrollmentIdentity struct {
	Email         string
	EmailVerified bool
	// Claims only holds claims of a provider proving its issuer.
	Claims map[string][]string
	// NewAccount tells that this sign-in created the account. A rule only
	// activates such an account: one that already existed inactive may have
	// been deactivated by an administrator.
	NewAccount bool
}

// MatchesEnrollmentRule tells whether the identity matches the rule, enabled
// or not.
func MatchesEnrollmentRule(rule EnrollmentRule, identity EnrollmentIdentity) bool {
	switch rule.Kind() {
	case EnrollmentKindEmailDomain:
		if !identity.EmailVerified {
			return false
		}
		_, domain, found := strings.Cut(NormalizeEmail(identity.Email), "@")
		return found && domain == NormalizeEnrollmentDomain(rule.Value())
	case EnrollmentKindClaim:
		return slices.Contains(identity.Claims[rule.Claim()], rule.Value())
	}
	return false
}

// NormalizeEnrollmentDomain accepts "Example.org" and "@example.org".
func NormalizeEnrollmentDomain(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "@"))
}

// EnrollmentRuleSettings is the representation of a rule in the provisioning
// API.
type EnrollmentRuleSettings struct {
	Name    string         `json:"name"`
	Kind    EnrollmentKind `json:"kind"`
	Claim   string         `json:"claim"`
	Value   string         `json:"value"`
	Role    string         `json:"role"`
	Enabled bool           `json:"enabled"`
}

type BaseEnrollmentRule struct {
	id        EnrollmentRuleID
	orgID     OrgID
	name      string
	kind      EnrollmentKind
	claim     string
	value     string
	role      string
	enabled   bool
	createdAt time.Time
	updatedAt time.Time
}

func NewEnrollmentRule(orgID OrgID, name string, kind EnrollmentKind, claim, value, role string, enabled bool) *BaseEnrollmentRule {
	return &BaseEnrollmentRule{
		id:        NewEnrollmentRuleID(),
		orgID:     orgID,
		name:      name,
		kind:      kind,
		claim:     claim,
		value:     value,
		role:      role,
		enabled:   enabled,
		createdAt: time.Now(),
		updatedAt: time.Now(),
	}
}

func (r *BaseEnrollmentRule) ID() EnrollmentRuleID { return r.id }
func (r *BaseEnrollmentRule) OrgID() OrgID         { return r.orgID }
func (r *BaseEnrollmentRule) Name() string         { return r.name }
func (r *BaseEnrollmentRule) Kind() EnrollmentKind { return r.kind }
func (r *BaseEnrollmentRule) Claim() string        { return r.claim }
func (r *BaseEnrollmentRule) Value() string        { return r.value }
func (r *BaseEnrollmentRule) Role() string         { return r.role }
func (r *BaseEnrollmentRule) Enabled() bool        { return r.enabled }
func (r *BaseEnrollmentRule) CreatedAt() time.Time { return r.createdAt }
func (r *BaseEnrollmentRule) UpdatedAt() time.Time { return r.updatedAt }

// SetID lets the provisioning API choose the identifier.
func (r *BaseEnrollmentRule) SetID(id EnrollmentRuleID) { r.id = id }

var _ EnrollmentRule = &BaseEnrollmentRule{}

type EnrollmentRuleOption func(*BaseEnrollmentRule)

func WithEnrollmentRuleName(name string) EnrollmentRuleOption {
	return func(r *BaseEnrollmentRule) { r.name = name }
}

func WithEnrollmentRuleKind(kind EnrollmentKind) EnrollmentRuleOption {
	return func(r *BaseEnrollmentRule) { r.kind = kind }
}

func WithEnrollmentRuleClaim(claim string) EnrollmentRuleOption {
	return func(r *BaseEnrollmentRule) { r.claim = claim }
}

func WithEnrollmentRuleValue(value string) EnrollmentRuleOption {
	return func(r *BaseEnrollmentRule) { r.value = value }
}

func WithEnrollmentRuleRole(role string) EnrollmentRuleOption {
	return func(r *BaseEnrollmentRule) { r.role = role }
}

func WithEnrollmentRuleEnabled(enabled bool) EnrollmentRuleOption {
	return func(r *BaseEnrollmentRule) { r.enabled = enabled }
}

// UpdateEnrollmentRule returns a copy of the given rule with the options
// applied and the updatedAt timestamp refreshed.
func UpdateEnrollmentRule(rule EnrollmentRule, opts ...EnrollmentRuleOption) *BaseEnrollmentRule {
	r := &BaseEnrollmentRule{
		id:        rule.ID(),
		orgID:     rule.OrgID(),
		name:      rule.Name(),
		kind:      rule.Kind(),
		claim:     rule.Claim(),
		value:     rule.Value(),
		role:      rule.Role(),
		enabled:   rule.Enabled(),
		createdAt: rule.CreatedAt(),
		updatedAt: time.Now(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}
