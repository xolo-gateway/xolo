package v1_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// callWithHeaders performs a JSON request with extra headers.
func callWithHeaders(t *testing.T, handler http.Handler, method, target string, headers map[string]string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func ifMatchHeader(etag string) map[string]string { return map[string]string{"If-Match": etag} }

func TestCommonReadsRoundTrip(t *testing.T) {
	env := newMultiTenantEnv(t)
	tenantID := uuid.NewString()
	orgID := uuid.NewString()
	tenantPath := "/v1/tenants/" + tenantID

	rec := call(t, env.handler, http.MethodPut, tenantPath, resource("reads"))
	assertStatus(t, rec, http.StatusOK)
	userID := createUser(t, env.handler, tenantID, "reader")

	for _, c := range []struct {
		name, collection, unit string
		body                   map[string]any
		changed                map[string]any
	}{
		{"tenant", "/v1/tenants", tenantPath, resource("reads"), resource("reads-renamed")},
		{"domain", tenantPath + "/domains", tenantPath + "/domains/reads.example.com", map[string]any{"status": "active"}, map[string]any{"status": "suspended"}},
		{"organization", tenantPath + "/organizations", tenantPath + "/organizations/" + orgID, resource("org"), resource("org-renamed")},
		{"member", tenantPath + "/members", tenantPath + "/members/" + userID, memberBody("reader@example.com"), memberBody("renamed@example.com")},
		{"membership", tenantPath + "/organizations/" + orgID + "/members", tenantPath + "/organizations/" + orgID + "/members/" + userID, membershipBody("member"), map[string]any{"role": "admin", "status": "suspended"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			put := call(t, env.handler, http.MethodPut, c.unit, c.body)
			assertStatus(t, put, http.StatusOK)
			etag := put.Header().Get("ETag")
			if !strings.HasPrefix(etag, `W/"`) {
				t.Fatalf("ETag: got %q", etag)
			}

			get := call(t, env.handler, http.MethodGet, c.unit, nil)
			assertStatus(t, get, http.StatusOK)
			if get.Header().Get("ETag") != etag || get.Body.String() != put.Body.String() {
				t.Fatalf("GET %s: got %s %s, want %s %s", c.unit, get.Header().Get("ETag"), get.Body.String(), etag, put.Body.String())
			}

			list := call(t, env.handler, http.MethodGet, c.collection, nil)
			assertStatus(t, list, http.StatusOK)
			var page struct {
				Items []struct {
					Representation json.RawMessage `json:"representation"`
					ETag           string          `json:"etag"`
				} `json:"items"`
				NextCursor *string `json:"next_cursor"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range page.Items {
				if item.ETag == etag {
					found = true
					if strings.TrimSpace(get.Body.String()) != string(item.Representation) {
						t.Errorf("list representation %s, unit %s", item.Representation, get.Body.String())
					}
				}
			}
			if !found || page.NextCursor != nil {
				t.Fatalf("list %s: %s", c.collection, list.Body.String())
			}

			// A matching condition on an identical body keeps the revision.
			same := callWithHeaders(t, env.handler, http.MethodPut, c.unit, ifMatchHeader(`"unknown", `+strings.TrimPrefix(etag, "W/")), c.body)
			assertStatus(t, same, http.StatusOK)
			if same.Header().Get("ETag") != etag {
				t.Fatalf("no-op changed the ETag: %s → %s", etag, same.Header().Get("ETag"))
			}
			changed := callWithHeaders(t, env.handler, http.MethodPut, c.unit, ifMatchHeader(etag), c.changed)
			assertStatus(t, changed, http.StatusOK)
			if changed.Header().Get("ETag") == etag {
				t.Fatal("a change must give a new ETag")
			}
			stale := callWithHeaders(t, env.handler, http.MethodPut, c.unit, ifMatchHeader(etag), c.changed)
			assertStatus(t, stale, http.StatusPreconditionFailed)
			assertErrorCode(t, stale, "precondition_failed")

			for _, headers := range []map[string]string{{"If-Match": `*, "x"`}, {"If-Match": "unquoted"}, {"If-None-Match": "*"}} {
				rec := callWithHeaders(t, env.handler, http.MethodPut, c.unit, headers, c.changed)
				assertStatus(t, rec, http.StatusBadRequest)
				assertErrorCode(t, rec, "invalid_precondition")
			}
			rec := call(t, env.handler, http.MethodGet, c.unit+"?limit=2", nil)
			assertStatus(t, rec, http.StatusBadRequest)
			assertErrorCode(t, rec, "invalid_parameter")
		})
	}

	rec = callWithHeaders(t, env.handler, http.MethodPut, "/v1/tenants/"+uuid.NewString(), ifMatchHeader("*"), resource("missing"))
	assertStatus(t, rec, http.StatusPreconditionFailed)
	rec = call(t, env.handler, http.MethodGet, tenantPath+"/members/"+uuid.NewString(), nil)
	assertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, "not_found")
	rec = call(t, env.handler, http.MethodGet, "/v1/tenants/"+uuid.NewString()+"/members", nil)
	assertStatus(t, rec, http.StatusNotFound)
	assertErrorCode(t, rec, "parent_not_found")
	rec = call(t, env.handler, http.MethodGet, tenantPath+"/domains/Reads.Example.com", nil)
	assertStatus(t, rec, http.StatusBadRequest)
	assertErrorCode(t, rec, "invalid_hostname")
}

func TestCommonPaginationAndEventsHTTP(t *testing.T) {
	env := newEnv(t)

	cursorRec := call(t, env.handler, http.MethodGet, "/v1/events/cursor", nil)
	assertStatus(t, cursorRec, http.StatusOK)
	start := decodeBody(t, cursorRec)["cursor"].(string)

	for i := range 3 {
		putOrganization(t, env.handler, env.tenantID, "page-"+string(rune('a'+i)))
	}

	first := call(t, env.handler, http.MethodGet, env.tenantBase+"/organizations?limit=2", nil)
	assertStatus(t, first, http.StatusOK)
	next, ok := decodeBody(t, first)["next_cursor"].(string)
	if !ok {
		t.Fatalf("expected a next cursor: %s", first.Body.String())
	}
	rest := call(t, env.handler, http.MethodGet, env.tenantBase+"/organizations?limit=2&cursor="+url.QueryEscape(next), nil)
	assertStatus(t, rest, http.StatusOK)
	if items := decodeBody(t, rest)["items"].([]any); len(items) != 1 {
		t.Fatalf("second page: %s", rest.Body.String())
	}

	events := call(t, env.handler, http.MethodGet, "/v1/events?limit=2&cursor="+url.QueryEscape(start), nil)
	assertStatus(t, events, http.StatusOK)
	body := decodeBody(t, events)
	if body["has_more"] != true || len(body["items"].([]any)) != 2 {
		t.Fatalf("first event page: %s", events.Body.String())
	}
	event := body["items"].([]any)[0].(map[string]any)
	if event["type"] != "organization.created.v1" || event["specversion"] != "1.0" {
		t.Fatalf("event: %v", event)
	}

	for _, c := range []struct {
		target, code string
		status       int
	}{
		{env.tenantBase + "/organizations?limit=0", "invalid_parameter", http.StatusBadRequest},
		{env.tenantBase + "/organizations?limit=1001", "invalid_parameter", http.StatusBadRequest},
		{env.tenantBase + "/organizations?limit=01", "invalid_parameter", http.StatusBadRequest},
		{env.tenantBase + "/organizations?limit=2&limit=3", "invalid_parameter", http.StatusBadRequest},
		{env.tenantBase + "/organizations?slug=acme", "invalid_parameter", http.StatusBadRequest},
		{env.tenantBase + "/organizations?cursor=", "invalid_cursor", http.StatusBadRequest},
		{env.tenantBase + "/organizations?cursor=garbage", "invalid_cursor", http.StatusBadRequest},
		{env.tenantBase + "/organizations?limit=3&cursor=" + url.QueryEscape(next), "invalid_cursor", http.StatusBadRequest},
		{env.tenantBase + "/members?limit=2&cursor=" + url.QueryEscape(next), "invalid_cursor", http.StatusBadRequest},
		{env.tenantBase + "/organizations?cursor=" + url.QueryEscape(start), "invalid_cursor", http.StatusBadRequest},
		{"/v1/events", "invalid_parameter", http.StatusBadRequest},
		{"/v1/events?cursor=" + url.QueryEscape(next), "invalid_cursor", http.StatusBadRequest},
		{"/v1/events/cursor?limit=1", "invalid_parameter", http.StatusBadRequest},
	} {
		rec := call(t, env.handler, http.MethodGet, c.target, nil)
		assertStatus(t, rec, c.status)
		assertErrorCode(t, rec, c.code)
	}

	// Once the history is purged, the old cursor expires.
	if _, err := env.store.PurgeEvents(t.Context(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rec := call(t, env.handler, http.MethodGet, "/v1/events?cursor="+url.QueryEscape(start), nil)
	assertStatus(t, rec, http.StatusGone)
	assertErrorCode(t, rec, "cursor_expired")
}

// reconstructionConsumer mirrors the documented consumer: an inventory taken
// after capturing a cursor, then events replayed by reading the resource.
type reconstructionConsumer struct {
	t       *testing.T
	handler http.Handler
	rows    map[string]string
	cursor  string
}

func (c *reconstructionConsumer) get(path string) (string, bool) {
	rec := call(c.t, c.handler, http.MethodGet, path, nil)
	if rec.Code == http.StatusNotFound {
		return "", false
	}
	assertStatus(c.t, rec, http.StatusOK)
	return strings.TrimSpace(rec.Body.String()) + " " + rec.Header().Get("ETag"), true
}

func (c *reconstructionConsumer) list(path string, visit func(key model.CommonKey, row string)) {
	cursor := ""
	for {
		target := path + "?limit=1"
		if cursor != "" {
			target += "&cursor=" + url.QueryEscape(cursor)
		}
		rec := call(c.t, c.handler, http.MethodGet, target, nil)
		assertStatus(c.t, rec, http.StatusOK)
		var page struct {
			Items []struct {
				Key            model.CommonKey `json:"key"`
				Representation json.RawMessage `json:"representation"`
				ETag           string          `json:"etag"`
			} `json:"items"`
			NextCursor *string `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			c.t.Fatal(err)
		}
		for _, item := range page.Items {
			visit(item.Key, string(item.Representation)+" "+item.ETag)
		}
		if page.NextCursor == nil {
			return
		}
		cursor = *page.NextCursor
	}
}

func resourcePath(family string, key model.CommonKey) string {
	base := "/v1/tenants/" + key.TenantID
	switch family {
	case model.FamilyTenantDomain:
		return base + "/domains/" + key.Hostname
	case model.FamilyOrganization:
		return base + "/organizations/" + key.OrganizationID
	case model.FamilyMember:
		return base + "/members/" + key.MemberID
	case model.FamilyOrganizationMembership:
		return base + "/organizations/" + key.OrganizationID + "/members/" + key.MemberID
	}
	return base
}

func (c *reconstructionConsumer) rebuild() {
	rec := call(c.t, c.handler, http.MethodGet, "/v1/events/cursor", nil)
	assertStatus(c.t, rec, http.StatusOK)
	c.cursor = decodeBody(c.t, rec)["cursor"].(string)
	c.rows = map[string]string{}
	c.list("/v1/tenants", func(tenant model.CommonKey, row string) {
		c.rows[resourcePath(model.FamilyTenant, tenant)] = row
		base := "/v1/tenants/" + tenant.TenantID
		for _, family := range []struct{ name, path string }{
			{model.FamilyTenantDomain, base + "/domains"},
			{model.FamilyOrganization, base + "/organizations"},
			{model.FamilyMember, base + "/members"},
		} {
			c.list(family.path, func(key model.CommonKey, row string) {
				c.rows[resourcePath(family.name, key)] = row
				if family.name == model.FamilyOrganization {
					c.list(resourcePath(family.name, key)+"/members", func(key model.CommonKey, row string) {
						c.rows[resourcePath(model.FamilyOrganizationMembership, key)] = row
					})
				}
			})
		}
	})
}

// poll applies the events since the checkpoint. checkpoint=false simulates a
// crash after applying them, before persisting the cursor.
func (c *reconstructionConsumer) poll(checkpoint bool) int {
	rec := call(c.t, c.handler, http.MethodGet, "/v1/events?cursor="+url.QueryEscape(c.cursor), nil)
	if rec.Code != http.StatusOK {
		return rec.Code
	}
	var page model.CommonEventPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		c.t.Fatal(err)
	}
	for _, event := range page.Items {
		path := resourcePath(event.Data.ResourceType, event.Data.Key)
		if row, ok := c.get(path); ok {
			c.rows[path] = row
		} else {
			delete(c.rows, path)
		}
	}
	if checkpoint {
		c.cursor = page.NextCursor
	}
	return http.StatusOK
}

func TestCommonReconstruction(t *testing.T) {
	env := newMultiTenantEnv(t)
	consumer := &reconstructionConsumer{t: t, handler: env.handler}
	consumer.rebuild()
	initial := consumer.cursor

	// Resources created after the inventory are discovered by the feed,
	// including the children of a new tenant.
	tenantID := uuid.NewString()
	assertStatus(t, call(t, env.handler, http.MethodPut, "/v1/tenants/"+tenantID, resource("rebuilt")), http.StatusOK)
	assertStatus(t, call(t, env.handler, http.MethodPut, "/v1/tenants/"+tenantID+"/domains/rebuilt.example.com", map[string]any{"status": "active"}), http.StatusOK)
	orgID := putOrganization(t, env.handler, tenantID, "rebuilt-org")
	userID, _ := addOrgMember(t, env.handler, tenantID, orgID, "rebuilt@example.com", "owner")

	if status := consumer.poll(false); status != http.StatusOK {
		t.Fatalf("poll: %d", status)
	}
	// A local change, made outside the provisioning API, is published too.
	org, err := env.store.GetOrgByID(t.Context(), model.OrgID(orgID))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.SaveOrg(t.Context(), model.UpdateOrganization(org, model.WithOrgName("Renamed locally"))); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, call(t, env.handler, http.MethodPut, "/v1/tenants/"+tenantID+"/members/"+userID, memberBody("renamed@example.com")), http.StatusOK)

	// Replaying from the old checkpoint after a crash converges.
	if status := consumer.poll(true); status != http.StatusOK {
		t.Fatalf("poll: %d", status)
	}
	if status := consumer.poll(true); status != http.StatusOK {
		t.Fatalf("poll: %d", status)
	}
	expected := &reconstructionConsumer{t: t, handler: env.handler}
	expected.rebuild()
	if len(consumer.rows) != len(expected.rows) {
		t.Fatalf("rows: got %d, want %d", len(consumer.rows), len(expected.rows))
	}
	for path, row := range expected.rows {
		if consumer.rows[path] != row {
			t.Errorf("%s: got %q, want %q", path, consumer.rows[path], row)
		}
	}

	// Once the history is gone the consumer is told so, and rebuilds.
	if _, err := env.store.PurgeEvents(t.Context(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SaveOrg(t.Context(), model.UpdateOrganization(org, model.WithOrgName("After purge"))); err != nil {
		t.Fatal(err)
	}
	stale := &reconstructionConsumer{t: t, handler: env.handler, cursor: initial, rows: expected.rows}
	if status := stale.poll(true); status != http.StatusGone {
		t.Fatalf("poll after purge: got %d, want 410", status)
	}
	stale.rebuild()
	if !strings.Contains(stale.rows[resourcePath(model.FamilyOrganization, model.CommonKey{TenantID: tenantID, OrganizationID: orgID})], "After purge") {
		t.Fatalf("rebuild missed the last change: %v", stale.rows)
	}
}
