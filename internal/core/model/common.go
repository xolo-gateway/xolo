package model

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// Status is the lifecycle state shared by every resource of the common
// provisioning contract.
type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
)

func (s Status) Valid() bool { return s == StatusActive || s == StatusSuspended }

// DeclaredStatus maps the active flag of tenants, organizations and users to a
// common status.
func DeclaredStatus(active bool) Status {
	if active {
		return StatusActive
	}
	return StatusSuspended
}

// TenantRole is the role of a user within its tenant.
type TenantRole string

const (
	TenantRoleOwner  TenantRole = "owner"
	TenantRoleMember TenantRole = "member"
)

func (r TenantRole) Valid() bool { return r == TenantRoleOwner || r == TenantRoleMember }

// MembershipRole is the common role of an organization membership. Each value
// maps to the builtin role of the same kind.
type MembershipRole string

const (
	MembershipRoleOwner  MembershipRole = BuiltinKindOwner
	MembershipRoleAdmin  MembershipRole = BuiltinKindAdmin
	MembershipRoleMember MembershipRole = BuiltinKindMember
)

func (r MembershipRole) Valid() bool {
	return r == MembershipRoleOwner || r == MembershipRoleAdmin || r == MembershipRoleMember
}

// CommonRoleOf derives the common role from the builtin roles of a membership,
// which must be loaded: the highest builtin kind wins, and a membership holding
// only custom roles is a member. Deriving it rather than storing it keeps a
// single source of truth with the web UI.
func CommonRoleOf(m Membership) MembershipRole {
	kinds := make([]string, 0, len(m.Roles()))
	for _, r := range m.Roles() {
		kinds = append(kinds, r.BuiltinKind())
	}
	return CommonRoleOfKinds(kinds...)
}

// CommonRoleOfKinds returns the highest common role among builtin role kinds.
func CommonRoleOfKinds(kinds ...string) MembershipRole {
	role := MembershipRoleMember
	for _, kind := range kinds {
		switch kind {
		case BuiltinKindOwner:
			return MembershipRoleOwner
		case BuiltinKindAdmin:
			role = MembershipRoleAdmin
		}
	}
	return role
}

var canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func parseUUID(s string) (string, error) {
	if !canonicalUUID.MatchString(s) {
		return "", fmt.Errorf("invalid UUID syntax")
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
func ParseTenantID(s string) (TenantID, error) { v, e := parseUUID(s); return TenantID(v), e }
func ParseOrgID(s string) (OrgID, error)       { v, e := parseUUID(s); return OrgID(v), e }
func ParseUserID(s string) (UserID, error)     { v, e := parseUUID(s); return UserID(v), e }

// Domain binds an explicit, instance-unique hostname to one tenant.
type Domain struct {
	Hostname string
	TenantID TenantID
	Status   Status
}

var hostnameLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// NormalizeHostname lower-cases a DNS hostname and rejects IP literals, ports
// and anything outside RFC 1123 labels.
func NormalizeHostname(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 253 || net.ParseIP(s) != nil {
		return "", fmt.Errorf("invalid hostname %q", s)
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) > 63 || !hostnameLabel.MatchString(label) {
			return "", fmt.Errorf("invalid hostname %q", s)
		}
	}
	return s, nil
}

// TenantHostPlaceholder is the marker of the legacy host pattern replaced by
// each tenant slug.
const TenantHostPlaceholder = "{tenant}"

// ExpandHostPattern builds the hostname a legacy host pattern gives to slug.
// The port of the pattern, if any, is dropped: domains are hostnames.
func ExpandHostPattern(pattern, slug string) (string, error) {
	host := strings.Replace(strings.TrimSpace(pattern), TenantHostPlaceholder, slug, 1)
	if h, _, found := strings.Cut(host, ":"); found {
		host = h
	}
	return NormalizeHostname(host)
}
