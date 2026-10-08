package model

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// CommonContractVersion is the version of the common contract Xolo targets.
const CommonContractVersion = "0.1.0-draft.1"

// Families of the common contract, as published in projections and events.
const (
	FamilyTenant                 = "tenant"
	FamilyTenantDomain           = "tenant_domain"
	FamilyOrganization           = "organization"
	FamilyMember                 = "member"
	FamilyOrganizationMembership = "organization_membership"
)

// Families of the business resources Xolo provisions besides the common
// contract, under /v1/xolo, with the same reads, conditions and events.
const (
	FamilyCustomRole  = "custom_role"
	FamilyApplication = "application"
	FamilyQuota       = "quota"
	FamilyAlert       = "alert"
	FamilyProvider    = "provider"
)

// CommonFamilies lists the families of the common contract, parents first.
var CommonFamilies = []string{FamilyTenant, FamilyTenantDomain, FamilyOrganization, FamilyMember, FamilyOrganizationMembership}

// BusinessFamilies lists the business families, parents first: role grants
// designate the models of providers, applications hold roles, quotas cap
// applications.
var BusinessFamilies = []string{FamilyProvider, FamilyCustomRole, FamilyApplication, FamilyQuota, FamilyAlert}

// IsBusinessFamily tells whether family is a business family.
func IsBusinessFamily(family string) bool { return slices.Contains(BusinessFamilies, family) }

// CommonKey identifies a resource independently of its mutable representation.
type CommonKey struct {
	TenantID       string `json:"tenant_id"`
	OrganizationID string `json:"organization_id,omitempty"`
	MemberID       string `json:"member_id,omitempty"`
	Hostname       string `json:"hostname,omitempty"`
	// ResourceID identifies a business resource within its parents.
	ResourceID string `json:"resource_id,omitempty"`
}

// CommonScope designates one collection: a family and the parents it hangs from.
type CommonScope struct {
	Family         string `json:"f"`
	TenantID       string `json:"t,omitempty"`
	OrganizationID string `json:"o,omitempty"`
}

// CommonItem is the projection of one resource.
type CommonItem struct {
	Key            CommonKey       `json:"key"`
	Representation json.RawMessage `json:"representation"`
	ETag           string          `json:"etag"`
}

type CommonPage struct {
	Items      []CommonItem `json:"items"`
	NextCursor *string      `json:"next_cursor"`
}

// CommonEventData carries no representation: consumers read the resource. A
// deletion carries no ETag.
type CommonEventData struct {
	ResourceType string    `json:"resource_type"`
	Key          CommonKey `json:"key"`
	ETag         string    `json:"etag,omitempty"`
}

// CommonEvent is a closed CloudEvents 1.0 profile. Time is informative only:
// Sequence alone orders events.
type CommonEvent struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Time            time.Time       `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	Sequence        string          `json:"sequence"`
	RequestID       string          `json:"requestid"`
	Data            CommonEventData `json:"data"`
}

type CommonEventPage struct {
	Items      []CommonEvent `json:"items"`
	NextCursor string        `json:"next_cursor"`
	HasMore    bool          `json:"has_more"`
}

// Event types: one event per resource and per commit.
const (
	CommonEventCreated = "created"
	CommonEventUpdated = "updated"
	CommonEventDeleted = "deleted"
)

// CommonEventType names the CloudEvents type of a change of family.
func CommonEventType(family, change string) string { return family + "." + change + ".v1" }

// CommonETag derives the validator of a projection from its revision: the
// sequence of the feed position that last changed it. Revisions are persisted
// and strictly increasing across the whole instance, so an ETag never repeats,
// even after a deletion, a recreation or a clock step.
func CommonETag(revision int64) string { return `W/"` + strconv.FormatInt(revision, 10) + `"` }

// MatchCondition implements the contract's deliberately weak If-Match comparison.
// A nil slice means absent; an empty present field is malformed.
type MatchCondition struct {
	Present, Any bool
	Tags         []string
}

func ParseMatchCondition(values []string) (MatchCondition, error) {
	c := MatchCondition{Present: len(values) > 0}
	if !c.Present {
		return c, nil
	}
	raw := strings.Trim(strings.Join(values, ","), " \t")
	if raw == "*" {
		c.Any = true
		return c, nil
	}
	for len(raw) > 0 {
		raw = strings.TrimLeft(raw, " \t")
		raw = strings.TrimPrefix(raw, "W/")
		if len(raw) == 0 || raw[0] != '"' {
			return c, fmt.Errorf("invalid entity tag")
		}
		end := 1
		for end < len(raw) && raw[end] != '"' {
			b := raw[end]
			if b < 0x21 || b == 0x7f {
				return c, fmt.Errorf("invalid entity tag")
			}
			end++
		}
		if end == len(raw) {
			return c, fmt.Errorf("unterminated entity tag")
		}
		c.Tags = append(c.Tags, raw[:end+1])
		raw = strings.TrimLeft(raw[end+1:], " \t")
		if raw == "" {
			return c, nil
		}
		if raw[0] != ',' {
			return c, fmt.Errorf("invalid tag separator")
		}
		raw = strings.TrimLeft(raw[1:], " \t")
		if raw == "" {
			return c, fmt.Errorf("empty entity tag")
		}
	}
	return c, fmt.Errorf("empty condition")
}

// Matches reports whether etag satisfies the condition. An absent condition
// always matches; a missing resource (empty etag) never matches a present one.
func (c MatchCondition) Matches(etag string) bool {
	if !c.Present {
		return true
	}
	if etag == "" {
		return false
	}
	if c.Any {
		return true
	}
	for _, tag := range c.Tags {
		if tag == strings.TrimPrefix(etag, "W/") {
			return true
		}
	}
	return false
}
