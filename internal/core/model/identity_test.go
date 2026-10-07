package model

import (
	"strings"
	"testing"
)

func TestIdentityValid(t *testing.T) {
	for _, c := range []struct {
		identity Identity
		valid    bool
	}{
		{Identity{"https://id.example.test/", "Case Sensitive "}, true},
		{Identity{"https://id.example.test", "s"}, true},
		{Identity{"https://id.example.test:8443/realms/a", "s"}, true},
		{Identity{"https://id.example.test/", strings.Repeat("s", 255)}, true},
		{Identity{"https://id.example.test/", strings.Repeat("s", 256)}, false},
		{Identity{"https://id.example.test/", ""}, false},
		{Identity{"https://id.example.test/", "a\nb"}, false},
		{Identity{"https://id.example.test/", "\xff"}, false},
		{Identity{"", "s"}, false},
		{Identity{"http://id.example.test/", "s"}, false},
		{Identity{"https:///path", "s"}, false},
		{Identity{"https://user@id.example.test/", "s"}, false},
		{Identity{"https://id.example.test/?q=1", "s"}, false},
		{Identity{"https://id.example.test/#f", "s"}, false},
		{Identity{" https://id.example.test/", "s"}, false},
		{Identity{"https://id.example.test/" + strings.Repeat("a", 2048), "s"}, false},
	} {
		if got := c.identity.Valid(); got != c.valid {
			t.Errorf("%q: got %v, want %v", c.identity, got, c.valid)
		}
	}
}

func TestIdentityIssuers(t *testing.T) {
	issuers := IdentityIssuers{"a": "https://x/", "b": "https://x/", "c": "https://y/", "d": ""}
	if issuer, ok := issuers.Issuer("a"); !ok || issuer != "https://x/" {
		t.Errorf("issuer of a: %q %v", issuer, ok)
	}
	if _, ok := issuers.Issuer("d"); ok {
		t.Error("an empty issuer proves nothing")
	}
	if got := issuers.Providers("https://x/"); strings.Join(got, ",") != "a,b" {
		t.Errorf("providers: %v", got)
	}
	if got := issuers.Providers(""); len(got) != 0 {
		t.Errorf("providers of no issuer: %v", got)
	}
}
