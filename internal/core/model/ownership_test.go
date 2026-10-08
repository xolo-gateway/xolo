package model

import (
	"context"
	"testing"
)

func TestOwnershipPolicyAllows(t *testing.T) {
	policy := OwnershipPolicy{FamilyMember: OwnerControlPlane, FamilyTenant: OwnerLocal}.Effective()
	cases := []struct {
		family    string
		authority Owner
		want      bool
	}{
		{FamilyMember, OwnerControlPlane, true},
		{FamilyMember, OwnerLocal, false},
		{FamilyTenant, OwnerLocal, true},
		{FamilyTenant, OwnerControlPlane, false},
		{FamilyOrganization, OwnerLocal, true},
		{FamilyOrganization, OwnerControlPlane, true},
	}
	for _, c := range cases {
		if got := policy.Allows(c.family, c.authority); got != c.want {
			t.Errorf("%s by %s: got %v, want %v", c.family, c.authority, got, c.want)
		}
	}
	var unconfigured OwnershipPolicy
	if !unconfigured.Allows(FamilyMember, OwnerControlPlane) || !unconfigured.Allows(FamilyMember, OwnerLocal) {
		t.Error("a nil policy must allow every authority")
	}
}

func TestOwnershipPolicyValidate(t *testing.T) {
	if err := (OwnershipPolicy{FamilySubscription: OwnerControlPlane}).Validate(); err != nil {
		t.Errorf("valid policy refused: %v", err)
	}
	for _, policy := range []OwnershipPolicy{{"unknown": OwnerLocal}, {FamilyMember: "external"}, {FamilyMember: ""}} {
		if err := policy.Validate(); err == nil {
			t.Errorf("%v accepted", policy)
		}
	}
}

func TestWriteAuthorityDefaultsToLocal(t *testing.T) {
	ctx := context.Background()
	if WriteAuthority(ctx) != OwnerLocal {
		t.Error("default authority must be local")
	}
	if WriteAuthority(WithWriteAuthority(ctx, OwnerControlPlane)) != OwnerControlPlane {
		t.Error("authority not carried")
	}
	if _, ok := SignInAccount(WithSignInAccount(ctx, "")); ok {
		t.Error("an empty sign-in account must not exempt anything")
	}
}
