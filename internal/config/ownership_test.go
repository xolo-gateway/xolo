package config

import (
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

func TestParse_OwnershipSharedByDefault(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	for family, owner := range conf.Ownership.Effective() {
		if owner != model.OwnerShared {
			t.Errorf("%s: got %q, want shared", family, owner)
		}
	}
}

func TestParse_Ownership(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)
	t.Setenv("XOLO_OWNERSHIP", "member=control_plane,organization_membership=control_plane,tenant=local")

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	effective := conf.Ownership.Effective()
	want := map[string]model.Owner{
		model.FamilyMember:                 model.OwnerControlPlane,
		model.FamilyOrganizationMembership: model.OwnerControlPlane,
		model.FamilyTenant:                 model.OwnerLocal,
		model.FamilyOrganization:           model.OwnerShared,
		model.FamilySubscription:           model.OwnerShared,
	}
	for family, owner := range want {
		if effective[family] != owner {
			t.Errorf("%s: got %q, want %q", family, effective[family], owner)
		}
	}
}

func TestParse_OwnershipInvalid(t *testing.T) {
	for _, value := range []string{"unknown=local", "member=external", "member=", "member"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("XOLO_SECRET_KEY", testSecretKey)
			t.Setenv("XOLO_OWNERSHIP", value)
			if _, err := Parse(); err == nil {
				t.Errorf("%q accepted", value)
			}
		})
	}
}
