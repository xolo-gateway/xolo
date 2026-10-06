package model

import "testing"

func TestCommonUUIDs(t *testing.T) {
	for _, id := range []string{string(NewTenantID()), string(NewOrgID()), string(NewUserID())} {
		if _, err := ParseTenantID(id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"d3jd1d1q0m0000000000", " 11111111-1111-4111-8111-111111111111", "AAAAAAAA-1111-4111-8111-111111111111", "{11111111-1111-4111-8111-111111111111}"} {
		if _, err := ParseTenantID(id); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
}
