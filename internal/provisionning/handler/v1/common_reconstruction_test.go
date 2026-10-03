package v1_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	adapter "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// This is a consumer fixture, not an additional production API or an external
// conformance runner. One mutex serializes GET+apply so old in-flight reads
// cannot overtake a later apply. State is persisted before its feed checkpoint.
type reconstructionState struct {
	Rows   map[string]model.CommonItem
	Seen   map[string]bool
	Cursor string
}
type reconstructionConsumer struct {
	mu                    sync.Mutex
	h                     http.Handler
	path                  string
	state                 reconstructionState
	crashBeforeCheckpoint bool
	unknown               []string
}

func newConsumer(h http.Handler, path string) *reconstructionConsumer {
	return &reconstructionConsumer{h: h, path: path, state: reconstructionState{Rows: map[string]model.CommonItem{}, Seen: map[string]bool{}}}
}
func fetchFixture(h http.Handler, path string, out any) error {
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
	if r.Code != 200 {
		return fmt.Errorf("HTTP %d: %s", r.Code, r.Body.String())
	}
	return json.Unmarshal(r.Body.Bytes(), out)
}
func (c *reconstructionConsumer) persist() error {
	raw, err := json.Marshal(c.state)
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.path+".tmp", raw, 0600); err != nil {
		return err
	}
	return os.Rename(c.path+".tmp", c.path)
}
func (c *reconstructionConsumer) resume() error {
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, &c.state)
}
func resourcePath(family string, k model.CommonKey) (string, bool) {
	base := "/v1/tenants/" + k.TenantID
	switch family {
	case "tenant":
		return base, true
	case "organization":
		return base + "/organizations/" + k.OrganizationID, true
	case "member":
		return base + "/members/" + k.MemberID, true
	case "tenant_domain":
		return base + "/domains/" + k.Hostname, true
	case "organization_membership":
		return base + "/organizations/" + k.OrganizationID + "/members/" + k.MemberID, true
	}
	return "", false
}
func (c *reconstructionConsumer) enumerate(state *reconstructionState, path, family string) error {
	next := ""
	for {
		query := "?limit=2"
		if next != "" {
			query += "&cursor=" + url.QueryEscape(next)
		}
		var page model.CommonPage
		if err := fetchFixture(c.h, path+query, &page); err != nil {
			return err
		}
		for _, item := range page.Items {
			p, _ := resourcePath(family, item.Key)
			state.Rows[p] = item
			if err := c.children(state, family, item.Key); err != nil {
				return err
			}
		}
		if page.NextCursor == nil {
			return nil
		}
		next = *page.NextCursor
	}
}
func (c *reconstructionConsumer) children(state *reconstructionState, family string, k model.CommonKey) error {
	base := "/v1/tenants/" + k.TenantID
	if family == "tenant" {
		for _, child := range []struct{ path, family string }{{"domains", "tenant_domain"}, {"members", "member"}, {"organizations", "organization"}} {
			if err := c.enumerate(state, base+"/"+child.path, child.family); err != nil {
				return err
			}
		}
	} else if family == "organization" {
		return c.enumerate(state, base+"/organizations/"+k.OrganizationID+"/members", "organization_membership")
	}
	return nil
}
func (c *reconstructionConsumer) replay(state *reconstructionState, durable bool) error {
	for {
		var page model.CommonEventPage
		if err := fetchFixture(c.h, "/v1/events?limit=2&cursor="+url.QueryEscape(state.Cursor), &page); err != nil {
			return err
		}
		for _, event := range page.Items {
			id := event.Source + " " + event.ID
			if state.Seen[id] {
				continue
			}
			path, known := resourcePath(event.Data.ResourceType, event.Data.Key)
			if !known {
				c.unknown = append(c.unknown, event.Type)
				state.Seen[id] = true
				continue
			}
			var representation json.RawMessage
			// A known reference's 404 or technical error is never a tombstone.
			rec := httptest.NewRecorder()
			c.h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			if rec.Code != 200 {
				return fmt.Errorf("required read HTTP %d", rec.Code)
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &representation); err != nil {
				return err
			}
			_, existed := state.Rows[path]
			state.Rows[path] = model.CommonItem{Key: event.Data.Key, Representation: representation, ETag: rec.Header().Get("ETag")}
			if !existed {
				if err := c.children(state, event.Data.ResourceType, event.Data.Key); err != nil {
					return err
				}
			}
			state.Seen[id] = true
		}
		if durable {
			if err := c.persist(); err != nil {
				return err
			}
			if c.crashBeforeCheckpoint {
				return errors.New("simulated crash after durable apply")
			}
		}
		state.Cursor = page.NextCursor
		if durable {
			if err := c.persist(); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
	}
}
func (c *reconstructionConsumer) rebuild() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var cursor map[string]string
	if err := fetchFixture(c.h, "/v1/events/cursor", &cursor); err != nil {
		return err
	}
	next := reconstructionState{Rows: map[string]model.CommonItem{}, Seen: map[string]bool{}, Cursor: cursor["cursor"]}
	if err := c.enumerate(&next, "/v1/tenants", "tenant"); err != nil {
		return err
	}
	if err := c.replay(&next, false); err != nil {
		return err
	}
	// Only a caught-up generation replaces authoritative state.
	c.state = next
	return c.persist()
}
func (c *reconstructionConsumer) poll() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.replay(&c.state, true)
}

type interceptHandler struct {
	h  http.Handler
	fn func(http.ResponseWriter, *http.Request) bool
}

func (h interceptHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.fn != nil && h.fn(w, r) {
		return
	}
	h.h.ServeHTTP(w, r)
}

func TestCommonReconstructionRecovery(t *testing.T) {
	h, db, _ := newTestHandler(t)
	c := newConsumer(h, filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, c.rebuild())
	old := c.state.Cursor
	// A parent and its children appear after the initial traversal. Replaying its
	// reference must discover every child, including pre-journal historical data.
	tenant := string(model.NewTenantID())
	parent := "/v1/tenants/" + tenant
	org, member := string(model.NewOrgID()), string(model.NewUserID())
	writes := []struct{ p, b string }{
		{parent, `{"slug":"later","name":"Later","status":"active"}`},
		{parent + "/organizations/" + org, `{"slug":"later","name":"Later","status":"active"}`},
		{parent + "/members/" + member, `{"email":"a@b","tenant_role":"member","status":"active"}`},
		{parent + "/domains/later.example.test", `{"status":"active"}`},
		{parent + "/organizations/" + org + "/members/" + member, `{"role":"member","status":"active"}`},
	}
	for _, w := range writes {
		assertCode(t, call(t, h, "PUT", w.p, w.b), 200, "")
	}
	// Failure retains the checkpoint; a stale known-reference 404 is not deletion.
	c.h = interceptHandler{h: h, fn: func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == parent {
			w.WriteHeader(404)
			return true
		}
		return false
	}}
	require.Error(t, c.poll())
	require.Equal(t, old, c.state.Cursor)
	c.h = h
	c.crashBeforeCheckpoint = true
	require.Error(t, c.poll())
	require.Equal(t, old, c.state.Cursor)
	resumed := newConsumer(h, c.path)
	require.NoError(t, resumed.resume())
	require.Equal(t, old, resumed.state.Cursor)
	require.NoError(t, resumed.poll())
	require.Len(t, resumed.state.Rows, 6)
	count := len(resumed.state.Seen)
	resumed.state.Cursor = old
	require.NoError(t, resumed.poll())
	require.Len(t, resumed.state.Seen, count)
	for _, w := range writes {
		live := call(t, h, "GET", w.p, nil)
		assertCode(t, live, 200, "")
		require.JSONEq(t, live.Body.String(), string(resumed.state.Rows[w.p].Representation))
		require.Equal(t, live.Header().Get("ETag"), resumed.state.Rows[w.p].ETag)
	}
	// Expiration rebuilds into a separate generation. A failed rebuild must not
	// replace the previous durable generation with a partial enumeration.
	require.NoError(t, adapter.NewStore(db).PurgeCommonEvents(t.Context(), time.Now().Add(time.Hour)))
	resumed.state.Cursor = old
	require.ErrorContains(t, resumed.poll(), "410")
	before := resumed.state
	resumed.h = interceptHandler{h: h, fn: func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == parent+"/members" {
			w.WriteHeader(500)
			return true
		}
		return false
	}}
	require.Error(t, resumed.rebuild())
	require.Equal(t, before, resumed.state)
	resumed.h = h
	require.NoError(t, resumed.rebuild())
	require.Len(t, resumed.state.Rows, 6)
	// An unknown independent extension can be reported and checkpointed once.
	original := resumed.state.Cursor
	resumed.h = interceptHandler{h: h, fn: func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/v1/events" {
			return false
		}
		_ = json.NewEncoder(w).Encode(model.CommonEventPage{Items: []model.CommonEvent{{ID: "extension", Source: "urn:test:extension", Type: "xolo.independent.v1", Data: model.CommonEventData{ResourceType: "extension"}}}, NextCursor: original, HasMore: false})
		return true
	}}
	require.NoError(t, resumed.poll())
	require.NoError(t, resumed.poll())
	require.Equal(t, []string{"xolo.independent.v1"}, resumed.unknown)
}
func TestCommonReconstructionDuringEnumeration(t *testing.T) {
	h, _, base := newTestHandler(t)
	injected := false
	c := newConsumer(nil, filepath.Join(t.TempDir(), "state.json"))
	c.h = interceptHandler{h: h, fn: func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/v1/tenants" || injected {
			return false
		}
		// Capture the old list, commit a new parent, then return the old page.
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		injected = true
		tid := string(model.NewTenantID())
		parent := "/v1/tenants/" + tid
		assertCode(t, call(t, h, "PUT", parent, `{"slug":"race","name":"Race","status":"active"}`), 200, "")
		assertCode(t, call(t, h, "PUT", parent+"/domains/race.example.test", `{"status":"suspended"}`), 200, "")
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return true
	}}
	require.NoError(t, c.rebuild())
	require.True(t, injected)
	require.Len(t, c.state.Rows, 3)
	require.Contains(t, c.state.Rows, base)
}
func TestCommonConsumerSerializesDelayedReads(t *testing.T) {
	h, _, base := newTestHandler(t)
	c := newConsumer(h, filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, c.rebuild())
	assertCode(t, call(t, h, "PUT", base, `{"slug":"default","name":"Earlier","status":"active"}`), 200, "")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c.h = interceptHandler{h: h, fn: func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != base {
			return false
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		once.Do(func() { close(entered); <-release })
		for k, values := range rec.Header() {
			w.Header()[k] = values
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return true
	}}
	done := make(chan error, 2)
	go func() { done <- c.poll() }()
	<-entered
	assertCode(t, call(t, h, "PUT", base, `{"slug":"default","name":"Latest","status":"active"}`), 200, "")
	go func() { done <- c.poll() }()
	select {
	case err := <-done:
		t.Fatalf("read overtook in-flight apply: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	require.Contains(t, string(c.state.Rows[base].Representation), "Latest")
	require.NotContains(t, string(c.state.Rows[base].Representation), "Earlier")
}
