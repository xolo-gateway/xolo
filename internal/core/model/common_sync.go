package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CommonKey identifies a resource independently of its mutable representation.
type CommonKey struct {
	ResourceID     string `json:"resource_id,omitempty"`
	TenantID       string `json:"tenant_id"`
	OrganizationID string `json:"organization_id,omitempty"`
	MemberID       string `json:"member_id,omitempty"`
	Hostname       string `json:"hostname,omitempty"`
}
type CommonScope struct {
	Family                   string
	TenantID, OrganizationID string
}
type CommonItem struct {
	Key            CommonKey       `json:"key"`
	Representation json.RawMessage `json:"representation"`
	ETag           string          `json:"etag"`
}
type CommonPage struct {
	Items      []CommonItem `json:"items"`
	NextCursor *string      `json:"next_cursor"`
}
type CommonEventData struct {
	ResourceType string    `json:"resource_type"`
	Key          CommonKey `json:"key"`
	ETag         string    `json:"etag"`
}
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

func CommonETag(updatedAt time.Time) string { return fmt.Sprintf("W/\"u-%d\"", updatedAt.UnixMicro()) }

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

type commonPUTKey struct{}

func WithCommonPUT(ctx context.Context) context.Context {
	return context.WithValue(ctx, commonPUTKey{}, true)
}
func IsCommonPUT(ctx context.Context) bool { v, _ := ctx.Value(commonPUTKey{}).(bool); return v }
