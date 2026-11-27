package authn

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestNewClaims(t *testing.T) {
	got, _ := NewClaims(map[string]any{
		"iss":            "https://id.example.test/",
		"email_verified": true,
		"groups":         []any{"staff", "ops"},
		"roles":          []string{"reader"},
		"exp":            float64(1700000000),
		"auth_time":      float64(1700000000),
		"nonce":          "abc",
		"mixed":          []any{"a", float64(1)},
		"address":        map[string]any{"country": "FR"},
		"nothing":        nil,
	})
	want := Claims{
		"iss":            {"https://id.example.test/"},
		"email_verified": {"true"},
		"groups":         {"staff", "ops"},
		"roles":          {"reader"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claims: got %v, want %v", got, want)
	}
}

func TestClaimsHas(t *testing.T) {
	claims, _ := NewClaims(map[string]any{"groups": []any{"staff", "ops"}, "iss": "https://id.example.test/"})
	for _, c := range []struct {
		name, value string
		want        bool
	}{
		{"groups", "ops", true},
		{"groups", "op", false},
		{"iss", "https://id.example.test/", true},
		{"absent", "x", false},
	} {
		if got := claims.Has(c.name, c.value); got != c.want {
			t.Errorf("Has(%q, %q) = %v", c.name, c.value, got)
		}
	}
	if (Claims)(nil).Has("iss", "x") {
		t.Error("an empty set holds nothing")
	}
}

// A provider can send anything: the session cookie must still fit, and the
// same token must give the same claims.
func TestNewSessionClaimsIsBounded(t *testing.T) {
	raw := map[string]any{"a_short": "kept", "huge": strings.Repeat("x", maxSessionClaimsSize+1)}
	for i := range 100 {
		raw["claim_"+strconv.Itoa(i)] = strings.Repeat("y", 64)
	}
	many := make([]any, maxSessionClaimValues+1)
	for i := range many {
		many[i] = "v"
	}
	raw["many"] = many

	claims, dropped := NewSessionClaims(raw, nil)
	if size := claimsSize(claims); size > maxSessionClaimsSize {
		t.Fatalf("claims take %d bytes", size)
	}
	if !claims.Has("a_short", "kept") {
		t.Fatal("a claim that fits was dropped")
	}
	if _, ok := claims["huge"]; ok {
		t.Fatal("an oversized claim was kept")
	}
	if !slices.Contains(dropped, "huge") || !slices.Contains(dropped, "many") {
		t.Fatalf("dropped claims not reported: %v", dropped)
	}
	again, _ := NewSessionClaims(raw, nil)
	if !reflect.DeepEqual(claims, again) {
		t.Fatal("the same token gave different claims")
	}
}

// A bearer token is never stored: a groups claim a session can not carry
// still matches.
func TestNewClaimsKeepsLargeGroups(t *testing.T) {
	groups := make([]any, 0, 100)
	for i := range 100 {
		groups = append(groups, "00000000-0000-0000-0000-"+strconv.Itoa(100000000000+i))
	}
	raw := map[string]any{"email": "a@example.org", "groups": groups}

	if claims, dropped := NewClaims(raw); !claims.Has("groups", groups[99].(string)) || len(dropped) > 0 {
		t.Fatal("a large groups claim was dropped from a token")
	}
	if _, dropped := NewSessionClaims(raw, nil); !slices.Contains(dropped, "groups") {
		t.Fatal("a session can not carry it")
	}

	raw["huge"] = strings.Repeat("x", maxClaimsSize+1)
	claims, dropped := NewClaims(raw)
	if size := claimsSize(claims); size > maxClaimsSize {
		t.Fatalf("claims take %d bytes", size)
	}
	if !slices.Equal(dropped, []string{"huge"}) {
		t.Fatalf("dropped: %v", dropped)
	}
}

// A token carrying more values than a claim may hold still matches the rules:
// the claim is reduced to the values they compare.
func TestPayloadClaimsKeepsComparedValues(t *testing.T) {
	groups := make([]string, 0, maxClaimValues+50)
	for i := range maxClaimValues + 50 {
		groups = append(groups, "group-"+strconv.Itoa(i))
	}
	payload, err := json.Marshal(map[string]any{"sub": "u", "groups": groups})
	if err != nil {
		t.Fatal(err)
	}
	source := PayloadClaims(payload)

	claims, dropped := source(nil)
	if claims.Has("groups", "group-300") || !slices.Equal(dropped, []string{"groups"}) {
		t.Fatalf("without rules: claims=%v dropped=%v", claims["groups"], dropped)
	}
	claims, dropped = source(map[string][]string{"groups": {"group-300", "absent"}})
	if !slices.Equal(claims["groups"], []string{"group-300"}) || len(dropped) > 0 {
		t.Fatalf("with a rule: claims=%v dropped=%v", claims["groups"], dropped)
	}
}

// The claims a rule compares come first, reduced to the values the rules
// compare when they do not fit whole.
func TestNewSessionClaimsKeepsComparedClaims(t *testing.T) {
	groups := make([]any, 0, 100)
	for i := range 100 {
		groups = append(groups, "group-"+strconv.Itoa(i))
	}
	raw := map[string]any{"address": strings.Repeat("a", maxSessionClaimsSize-10), "groups": groups}

	claims, dropped := NewSessionClaims(raw, map[string][]string{"groups": {"group-99", "absent"}})
	if !reflect.DeepEqual(claims["groups"], []string{"group-99"}) {
		t.Fatalf("groups: %v", claims["groups"])
	}
	if !slices.Contains(dropped, "address") || slices.Contains(dropped, "groups") {
		t.Fatalf("dropped: %v", dropped)
	}
	if size := claimsSize(claims); size > maxSessionClaimsSize {
		t.Fatalf("claims take %d bytes", size)
	}

	small := map[string]any{"groups": []any{"ops", "staff"}}
	whole, _ := NewSessionClaims(small, map[string][]string{"groups": {"ops"}})
	if !whole.Has("groups", "staff") {
		t.Fatal("a compared claim that fits was reduced")
	}
}

// A bearer token decodes its claims only when asked.
func TestClaimSet(t *testing.T) {
	calls := 0
	user := &User{ClaimsSource: func(map[string][]string) (Claims, []string) { calls++; return Claims{"groups": {"ops"}}, nil }}
	if calls != 0 {
		t.Fatal("claims decoded eagerly")
	}
	if claims, _ := user.ClaimSet(nil); !claims.Has("groups", "ops") || calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	stored := &User{Claims: Claims{"groups": {"staff"}}, ClaimsSource: func(map[string][]string) (Claims, []string) { t.Fatal("source used"); return nil, nil }}
	if claims, _ := stored.ClaimSet(nil); !claims.Has("groups", "staff") {
		t.Fatal("stored claims ignored")
	}
}

func claimsSize(claims Claims) int {
	size := 0
	for name, values := range claims {
		size += len(name)
		for _, value := range values {
			size += len(value)
		}
	}
	return size
}

// A session cookie stores the user with gob.
func TestUserClaimsSurviveEncoding(t *testing.T) {
	user := User{Subject: "u", Claims: Claims{"groups": {"staff"}}}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&user); err != nil {
		t.Fatal(err)
	}
	var decoded User
	if err := gob.NewDecoder(&buf).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Claims.Has("groups", "staff") {
		t.Fatalf("claims lost: %v", decoded.Claims)
	}
}
