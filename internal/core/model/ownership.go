package model

import (
	"context"
	"fmt"
)

type Owner string

const (
	OwnerLocal        Owner = "local"
	OwnerControlPlane Owner = "control_plane"
)

var OwnershipFamilies = []string{"tenant", "organization", "member", "tenant_domain", "organization_membership", "subscription", "custom_role", "application", "quota", "alert", "provider"}

type OwnershipPolicy map[string]Owner

func (p OwnershipPolicy) Effective() OwnershipPolicy {
	out := OwnershipPolicy{}
	for _, family := range OwnershipFamilies {
		out[family] = OwnerLocal
	}
	for family, owner := range p {
		out[family] = owner
	}
	return out
}
func (p OwnershipPolicy) Validate() error {
	for family, owner := range p {
		known := false
		for _, f := range OwnershipFamilies {
			known = known || family == f
		}
		if !known || (owner != OwnerLocal && owner != OwnerControlPlane) {
			return fmt.Errorf("invalid ownership policy %q=%q", family, owner)
		}
	}
	return nil
}

type authorityKey struct{}

// WithWriteAuthority is set by trusted transport/composition code, never from payloads.
func WithWriteAuthority(ctx context.Context, owner Owner) context.Context {
	return context.WithValue(ctx, authorityKey{}, owner)
}
func WriteAuthority(ctx context.Context) Owner {
	if v, ok := ctx.Value(authorityKey{}).(Owner); ok {
		return v
	}
	return OwnerLocal
}
