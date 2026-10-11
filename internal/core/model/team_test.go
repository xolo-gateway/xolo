package model

import (
	"testing"
)

func TestParseModelName(t *testing.T) {
	isTeam := func(org, segment string) bool { return org == "acme" && segment == "legal" }
	cases := []struct {
		name string
		want ModelName
		ok   bool
	}{
		{"gpt-4o", ModelName{Local: "gpt-4o"}, true},
		{"acme/gpt-4o", ModelName{Org: "acme", Local: "gpt-4o"}, true},
		{"acme/legal/gpt-4o", ModelName{Org: "acme", Team: "legal", Local: "gpt-4o"}, true},
		// A proxy name holding a slash stays local when its first segment is
		// no team of the organization.
		{"acme/meta/llama-3", ModelName{Org: "acme", Local: "meta/llama-3"}, true},
		{"other/legal/gpt-4o", ModelName{Org: "other", Local: "legal/gpt-4o"}, true},
		{"~/assistant", ModelName{Org: PersonalModelOrg, Local: "assistant"}, true},
		{"~/legal/x", ModelName{Org: PersonalModelOrg, Local: "legal/x"}, true},
		{"acme/legal/", ModelName{Org: "acme", Local: "legal/"}, true},
		{"", ModelName{}, false},
		{"/gpt", ModelName{}, false},
		{"acme/", ModelName{}, false},
	}
	for _, c := range cases {
		got, ok := ParseModelName(c.name, isTeam)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseModelName(%q) = %+v, %v; want %+v, %v", c.name, got, ok, c.want, c.ok)
		}
		if ok && c.name != "" && got.String() != c.name {
			t.Errorf("ModelName(%q).String() = %q", c.name, got.String())
		}
	}
	if got, _ := ParseModelName("acme/legal/gpt-4o", nil); got.Team != "" {
		t.Errorf("a nil isTeam reads no team segment, got %+v", got)
	}
	n, _ := ParseModelName("acme/legal/gpt-4o", isTeam)
	if n.OrgLevel().String() != "acme/gpt-4o" {
		t.Errorf("OrgLevel = %q", n.OrgLevel().String())
	}
}

func TestModelNameConflicts(t *testing.T) {
	if !ModelNameConflicts("meta", "meta/llama-3") {
		t.Error("a proxy name starting with a team slug and a slash conflicts")
	}
	if ModelNameConflicts("meta", "metallama") || ModelNameConflicts("meta", "llama/meta") {
		t.Error("only the first segment matters")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Équipe juridique":   "equipe-juridique",
		"  R&D -- Labs  ":    "r-d-labs",
		"Application CI/CD":  "application-ci-cd",
		"!!!":                "fallback",
		"Ça marche, à 100 %": "ca-marche-a-100",
	}
	for in, want := range cases {
		if got := Slugify(in, "fallback"); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTeamMemberScopeID(t *testing.T) {
	id := TeamMemberScopeID("t1", "u1")
	team, user, ok := ParseTeamMemberScopeID(id)
	if !ok || team != "t1" || user != "u1" {
		t.Errorf("round trip of %q gave %q, %q, %v", id, team, user, ok)
	}
	if _, _, ok := ParseTeamMemberScopeID("t1"); ok {
		t.Error("a scope id without separator is invalid")
	}
}

func TestQuotaCounters(t *testing.T) {
	record := func(user UserID, app ApplicationID, team TeamID) *BaseUsageRecord {
		return NewUsageRecord(user, app, "org", team, "p", "m", "acme/gpt", "", 10, 0, 10, 100, "EUR", CostSourceComputed, "")
	}
	got := QuotaCounters(record("u1", "", "t1"))
	want := []QuotaCounter{{QuotaScopeOrg, "org"}, {QuotaScopeUser, "u1"}, {QuotaScopeTeam, "t1"}, {QuotaScopeTeamMember, "t1:u1"}}
	if len(got) != len(want) {
		t.Fatalf("user in a team: got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("counter %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	got = QuotaCounters(record("shadow", "a1", "t1"))
	want = []QuotaCounter{{QuotaScopeOrg, "org"}, {QuotaScopeApplication, "a1"}, {QuotaScopeTeam, "t1"}}
	if len(got) != len(want) {
		t.Fatalf("application bound to a team: got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("counter %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	if got := QuotaCounters(record("u1", "", "")); len(got) != 2 {
		t.Errorf("no team, no team counter: got %+v", got)
	}
	covered := record("u1", "", "t1")
	covered.SetPlanCovered(true)
	if got := QuotaCounters(covered); len(got) != 0 {
		t.Errorf("subscription usage feeds no counter: got %+v", got)
	}
}

func TestQuotaCascade(t *testing.T) {
	if !QuotaCascade("").Valid() || QuotaCascade("").Normalize() != QuotaCascadeFree {
		t.Error("the empty cascade is free")
	}
	if QuotaCascade("other").Valid() {
		t.Error("unknown cascade accepted")
	}
	if !QuotaScopeOrg.HasChildren() || !QuotaScopeTeam.HasChildren() || QuotaScopeTeamMember.HasChildren() || QuotaScopeUser.HasChildren() {
		t.Error("only organizations and teams have children budgets")
	}
}
