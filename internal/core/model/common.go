package model

import (
	"fmt"
	"github.com/google/uuid"
	"regexp"
)

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
