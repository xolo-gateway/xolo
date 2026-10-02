package gorm

import (
	"context"
	"testing"

	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/ncruces/go-sqlite3/gormlite"
	gormpkg "gorm.io/gorm"
)

// TestUpgradeNormalizesInviteeEmails covers 202609300001 on an instance that
// already holds invitations written before addresses were normalized. A fresh
// install goes through InitSchema and never runs it.
func TestUpgradeNormalizesInviteeEmails(t *testing.T) {
	db, err := gormpkg.Open(gormlite.Open(":memory:"), &gormpkg.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	if err := db.AutoMigrate(&InviteToken{}); err != nil {
		t.Fatalf("migrate invite_tokens: %v", err)
	}
	if err := db.Exec("CREATE TABLE migrations (id VARCHAR(255) PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create migrations table: %v", err)
	}
	applied := []string{
		"202602010001", "202506040001", "202606080001", "202606180001", "202606250001",
		"202506290001", "202606290001", "202607010001", "202607050001", "202607050002",
		"202607170001", "202607210001", "202607220001", "202608130001", "202608220001",
		"202609040001", "202609150001", "202609150002", "202609170001", "202609170002",
		"202609240001",
	}
	for _, id := range applied {
		if err := db.Exec("INSERT INTO migrations (id) VALUES (?)", id).Error; err != nil {
			t.Fatalf("mark %s applied: %v", id, err)
		}
	}

	// The rows reference no real organization; only invitee_email matters here.
	if err := db.Exec("PRAGMA foreign_keys=off").Error; err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}

	mixed, blank := " Jean.Dupont@Corp.tld ", "  "
	for _, invite := range []InviteToken{
		{ID: "mixed", OrgID: "org-1", Role: "member", CreatedByUserID: "u", InviteeEmail: &mixed},
		{ID: "blank", OrgID: "org-1", Role: "member", CreatedByUserID: "u", InviteeEmail: &blank},
		{ID: "open", OrgID: "org-1", Role: "member", CreatedByUserID: "u"},
	} {
		if err := db.Create(&invite).Error; err != nil {
			t.Fatalf("seed %s: %v", invite.ID, err)
		}
	}

	if _, err := createGetDatabase(db)(context.Background()); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	// "" stands for NULL: a blank address names nobody, like an open link.
	want := map[string]string{"mixed": "jean.dupont@corp.tld", "blank": "", "open": ""}
	for id, expected := range want {
		var got InviteToken
		if err := db.First(&got, "id = ?", id).Error; err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if actual := got.InviteeEmail; (actual == nil) != (expected == "") || (actual != nil && *actual != expected) {
			t.Errorf("%s: invitee_email = %v, want %q", id, actual, expected)
		}
	}
}
