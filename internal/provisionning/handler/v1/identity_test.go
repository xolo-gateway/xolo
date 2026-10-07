package v1_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func identityBody(email string, identity any) map[string]any {
	body := memberBody(email)
	body["identity"] = identity
	return body
}

// TestMemberIdentityHTTP covers the identity of a member PUT: an object
// declares it, null removes it, and an absent field keeps it.
func TestMemberIdentityHTTP(t *testing.T) {
	env := newEnv(t)
	memberID := uuid.NewString()
	unit := env.tenantBase + "/members/" + memberID
	declared := map[string]any{"issuer": "https://id.example.test/", "subject": "Case Sensitive "}

	put := call(t, env.handler, http.MethodPut, unit, identityBody("jane@acme.tld", declared))
	assertStatus(t, put, http.StatusOK)
	etag := put.Header().Get("ETag")
	if got := decodeBody(t, put)["identity"]; !jsonEqual(got, declared) {
		t.Fatalf("identity: got %v, want %v", got, declared)
	}

	get := call(t, env.handler, http.MethodGet, unit, nil)
	assertStatus(t, get, http.StatusOK)
	if get.Body.String() != put.Body.String() || get.Header().Get("ETag") != etag {
		t.Fatalf("GET: got %s %s", get.Header().Get("ETag"), get.Body.String())
	}

	list := call(t, env.handler, http.MethodGet, env.tenantBase+"/members", nil)
	assertStatus(t, list, http.StatusOK)
	if !strings.Contains(list.Body.String(), `"identity":{"issuer":"https://id.example.test/","subject":"Case Sensitive "}`) {
		t.Errorf("list: %s", list.Body.String())
	}

	again := call(t, env.handler, http.MethodPut, unit, identityBody("jane@acme.tld", declared))
	assertStatus(t, again, http.StatusOK)
	if again.Header().Get("ETag") != etag {
		t.Errorf("a repeated PUT changed the ETag: %s → %s", etag, again.Header().Get("ETag"))
	}

	t.Run("invalid identities change nothing", func(t *testing.T) {
		for name, identity := range map[string]any{
			"empty object":       map[string]any{},
			"missing subject":    map[string]any{"issuer": "https://id.example.test/"},
			"empty subject":      map[string]any{"issuer": "https://id.example.test/", "subject": ""},
			"extra field":        map[string]any{"issuer": "https://id.example.test/", "subject": "s", "name": "x"},
			"http issuer":        map[string]any{"issuer": "http://id.example.test/", "subject": "s"},
			"issuer with query":  map[string]any{"issuer": "https://id.example.test/?a=b", "subject": "s"},
			"issuer with spaces": map[string]any{"issuer": " https://id.example.test/", "subject": "s"},
			"userinfo":           map[string]any{"issuer": "https://user@id.example.test/", "subject": "s"},
			"number subject":     map[string]any{"issuer": "https://id.example.test/", "subject": 1},
			"boolean":            true,
			"array":              []any{},
			"string":             "https://id.example.test/",
		} {
			t.Run(name, func(t *testing.T) {
				rec := call(t, env.handler, http.MethodPut, unit, identityBody("jane@acme.tld", identity))
				assertStatus(t, rec, http.StatusBadRequest)
				assertErrorCode(t, rec, "invalid_representation")
			})
		}
		rec := call(t, env.handler, http.MethodPut, unit, `{"email":"jane@acme.tld","tenant_role":"member","status":"active","identity":{"issuer":"https://id.example.test/","subject":"\ud800"}}`)
		assertStatus(t, rec, http.StatusBadRequest)

		get := call(t, env.handler, http.MethodGet, unit, nil)
		if get.Header().Get("ETag") != etag || get.Body.String() != put.Body.String() {
			t.Fatalf("an invalid PUT changed the member: %s", get.Body.String())
		}
	})

	t.Run("an identity owned by another member is a conflict", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodPut, env.tenantBase+"/members/"+uuid.NewString(), identityBody("other@acme.tld", declared))
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")
	})

	t.Run("an email held by another member is a conflict", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodPut, env.tenantBase+"/members/"+uuid.NewString(), memberBody("JANE@acme.tld"))
		assertStatus(t, rec, http.StatusConflict)
		assertErrorCode(t, rec, "conflict")
		errorBody, _ := decodeBody(t, rec)["error"].(map[string]any)
		if got, want := errorBody["message"], `email "JANE@acme.tld" is already used by another user`; got != want {
			t.Errorf("message: got %q, want %q", got, want)
		}
	})

	t.Run("an absent identity keeps it", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodPut, unit, memberBody("jane@acme.tld"))
		assertStatus(t, rec, http.StatusOK)
		if rec.Header().Get("ETag") != etag {
			t.Fatalf("an absent identity changed the member: %s", rec.Body.String())
		}
	})

	t.Run("null removes it", func(t *testing.T) {
		rec := call(t, env.handler, http.MethodPut, unit, identityBody("jane@acme.tld", nil))
		assertStatus(t, rec, http.StatusOK)
		if _, present := decodeBody(t, rec)["identity"]; present || rec.Header().Get("ETag") == etag {
			t.Fatalf("null kept the identity: %s", rec.Body.String())
		}
		again := call(t, env.handler, http.MethodPut, unit, identityBody("jane@acme.tld", nil))
		assertStatus(t, again, http.StatusOK)
		if again.Header().Get("ETag") != rec.Header().Get("ETag") {
			t.Error("removing a missing identity is a no-op")
		}
	})
}

func jsonEqual(a, b any) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && string(x) == string(y)
}
