package rbac

import "testing"

func TestNewPermissionSetWriteImpliesRead(t *testing.T) {
	set := NewPermissionSet([]string{string(PermMembersWrite)}, nil)

	if !set.Has(PermMembersWrite) {
		t.Error("expected write permission to be granted")
	}
	if !set.Has(PermMembersRead) {
		t.Error("expected write to imply read")
	}
	if set.Has(PermProvidersRead) {
		t.Error("did not expect unrelated permission")
	}
}

func TestNewPermissionSetIgnoresUnknownCodes(t *testing.T) {
	set := NewPermissionSet([]string{"bogus:code", string(PermUsageRead)}, nil)

	if set.Has(Permission("bogus:code")) {
		t.Error("unknown code should not be granted")
	}
	if !set.Has(PermUsageRead) {
		t.Error("known code should be granted")
	}
}

func TestOwnerPermissionSetGrantsEverything(t *testing.T) {
	set := OwnerPermissionSet()

	if !set.IsOwner() {
		t.Error("expected owner set")
	}
	if !set.Has(PermSettingsWrite) {
		t.Error("owner should have every permission")
	}
	if !set.CanUseModel("", "any", ModelKindLLM) || !set.CanUseModel("", "any", ModelKindVirtual) {
		t.Error("owner should have access to any model")
	}
}

func TestCanUseModelThroughTeams(t *testing.T) {
	set := NewPermissionSet(nil, []TeamAccess{
		{TeamID: "t-legal", Slug: "legal", Grants: []ModelGrant{{ModelID: "m1", Kind: ModelKindLLM}}},
		{TeamID: "t-rd", Slug: "rd", Grants: []ModelGrant{{ModelID: "m1", Kind: ModelKindLLM}, {ModelID: "v1", Kind: ModelKindVirtual}}},
	})

	if !set.CanUseModel("t-legal", "m1", ModelKindLLM) {
		t.Error("expected the team grant to give access")
	}
	if set.CanUseModel("", "m1", ModelKindLLM) {
		t.Error("a team grant must not give access outside the team")
	}
	if set.CanUseModel("t-legal", "v1", ModelKindVirtual) {
		t.Error("a grant of another team must not give access")
	}
	if set.CanUseModel("t-legal", "m1", ModelKindVirtual) {
		t.Error("kind should matter")
	}
	if set.CanUseModel("t-other", "m1", ModelKindLLM) {
		t.Error("a team the principal does not belong to grants nothing")
	}
	if got := set.TeamsGranting("m1", ModelKindLLM); len(got) != 2 || got[0].Slug != "legal" || got[1].Slug != "rd" {
		t.Errorf("expected both teams ordered by slug, got %+v", got)
	}
	if !set.HasAnyModelAccess(ModelKindVirtual) || set.HasOrgWideModelAccess(ModelKindLLM) {
		t.Error("team grants are not an organization-wide access")
	}
}

func TestOrgWideModelAccess(t *testing.T) {
	set := NewPermissionSet([]string{string(PermModelUseOrg)}, nil)

	if !set.CanUseModel("", "m1", ModelKindLLM) {
		t.Error("model:use:org gives access to every real model")
	}
	if set.CanUseModel("", "v1", ModelKindVirtual) {
		t.Error("model:use:org does not cover virtual models")
	}
}

func TestTeamScopedPermissions(t *testing.T) {
	set := NewPermissionSet([]string{string(PermUsageRead)}, []TeamAccess{
		{TeamID: "t-legal", Slug: "legal", Codes: []string{string(PermMembersWrite), string(PermProvidersWrite)}},
	})

	if !set.HasInTeam("t-legal", PermMembersWrite) || !set.HasInTeam("t-legal", PermMembersRead) {
		t.Error("a team role grants its team-scopable permissions on the team, write implying read")
	}
	if set.HasInTeam("t-legal", PermProvidersWrite) {
		t.Error("a permission that cannot be team-scoped is dropped from a team role")
	}
	if set.Has(PermMembersWrite) || set.HasInTeam("t-other", PermMembersWrite) {
		t.Error("a team role grants nothing on the organization or another team")
	}
	if !set.HasInTeam("t-other", PermUsageRead) {
		t.Error("an organization permission holds on every team")
	}
	if !set.HasInAnyTeam(PermMembersWrite) {
		t.Error("expected the team permission to be found")
	}
}

func TestIsKnown(t *testing.T) {
	if !IsKnown(string(PermModelUseOrg)) {
		t.Error("declared permission should be known")
	}
	if IsKnown("nonexistent:perm") {
		t.Error("undeclared permission should not be known")
	}
}

func TestCatalogOnlyKnownCodes(t *testing.T) {
	for _, group := range Catalog() {
		for _, def := range group.Perms {
			if !IsKnown(string(def.Code)) {
				t.Errorf("catalog code %q reported as unknown", def.Code)
			}
		}
	}
}
