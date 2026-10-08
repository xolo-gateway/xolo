package model

import "testing"

func TestParseBusinessKey(t *testing.T) {
	if key, isUUID, err := ParseBusinessKey("2ed07e0a-d163-4ab4-a36f-ebda3e06e51a"); err != nil || !isUUID || key == "" {
		t.Errorf("uuid: %q %v %v", key, isUUID, err)
	}
	local := string(NewRoleID())
	if key, isUUID, err := ParseBusinessKey(local); err != nil || isUUID || key != local {
		t.Errorf("xid: %q %v %v", key, isUUID, err)
	}
	for _, raw := range []string{"", "2ED07E0A-D163-4AB4-A36F-EBDA3E06E51A", "not-a-key", local + "x", "../roles"} {
		if _, _, err := ParseBusinessKey(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}
