package model

import (
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
)

func (s Status) Valid() bool { return s == StatusActive || s == StatusSuspended }
func DeclaredStatus(active bool) Status {
	if active {
		return StatusActive
	}
	return StatusSuspended
}

type TenantRole string

const (
	TenantRoleOwner  TenantRole = "owner"
	TenantRoleMember TenantRole = "member"
)

func (r TenantRole) Valid() bool { return r == TenantRoleOwner || r == TenantRoleMember }

type MembershipRole string

const (
	MembershipRoleOwner  MembershipRole = "owner"
	MembershipRoleAdmin  MembershipRole = "admin"
	MembershipRoleMember MembershipRole = "member"
)

func (r MembershipRole) Valid() bool {
	return r == MembershipRoleOwner || r == MembershipRoleAdmin || r == MembershipRoleMember
}
func NormalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

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

var hostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

func NormalizeHostname(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 253 || s == "" || net.ParseIP(s) != nil {
		return "", fmt.Errorf("invalid hostname")
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) > 63 || !hostnamePattern.MatchString(label) {
			return "", fmt.Errorf("invalid hostname")
		}
	}
	return s, nil
}
