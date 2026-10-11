package gorm

import (
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// TestQuotaUsageRows_BothIDsSkipsUserScope pins the else if in quotaUsageRows:
// a record carrying both an application id and a user id must emit exactly
// two rows (org + application), never the shadow user's user counter. This is
// the structural enforcement of the "application records do not feed the
// shadow user's user counter" invariant (issue #64); the cache layer has an
// equivalent test on cache keys, but a regression that converted the
// `else if` to two `if`s would not be caught at the gorm layer otherwise.
func TestQuotaUsageRows_BothIDsSkipsUserScope(t *testing.T) {
	record := model.NewUsageRecord(
		"usr-shadow-app-1", "app-1", "org-1", "", "provider", "model",
		"fast", "", 10, 0, 10, 700, "USD", model.CostSourceComputed, "")

	rows := quotaUsageRows(record)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (org + application), got %d: %+v", len(rows), rows)
	}
	scopes := map[string]string{}
	for _, r := range rows {
		scopes[r.Scope] = r.ScopeID
	}
	if scopes[string(model.QuotaScopeOrg)] != "org-1" {
		t.Errorf("missing or wrong org row, scopes = %+v", scopes)
	}
	if scopes[string(model.QuotaScopeApplication)] != "app-1" {
		t.Errorf("missing or wrong application row, scopes = %+v", scopes)
	}
	if _, ok := scopes[string(model.QuotaScopeUser)]; ok {
		t.Errorf("shadow user row leaked: scopes = %+v", scopes)
	}
}

// TestQuotaUsageRows_UserOnly exercises the else if branch's other side: a
// record without an application id and with a user id emits org + user, no
// application row. This is the pre-existing user-scope path; the assertion is
// trivial today but pinning it locks the shape the backfill expects.
func TestQuotaUsageRows_UserOnly(t *testing.T) {
	record := model.NewUsageRecord(
		"user-1", "", "org-1", "", "provider", "model",
		"fast", "", 10, 0, 10, 700, "USD", model.CostSourceComputed, "")

	rows := quotaUsageRows(record)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (org + user), got %d: %+v", len(rows), rows)
	}
	scopes := map[string]string{}
	for _, r := range rows {
		scopes[r.Scope] = r.ScopeID
	}
	if scopes[string(model.QuotaScopeUser)] != "user-1" {
		t.Errorf("missing user row, scopes = %+v", scopes)
	}
	if _, ok := scopes[string(model.QuotaScopeApplication)]; ok {
		t.Errorf("unexpected application row, scopes = %+v", scopes)
	}
}

// TestQuotaUsageRows_NoPrincipal covers the orphan path: a record with no
// user id and no application id only emits the org row. Mirrors the cache
// test for the same shape.
func TestQuotaUsageRows_NoPrincipal(t *testing.T) {
	record := model.NewUsageRecord(
		"", "", "org-1", "", "provider", "model",
		"fast", "", 10, 0, 10, 700, "USD", model.CostSourceComputed, "")

	rows := quotaUsageRows(record)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (org only), got %d: %+v", len(rows), rows)
	}
	if rows[0].Scope != string(model.QuotaScopeOrg) {
		t.Errorf("org scope = %q, want org", rows[0].Scope)
	}
}
