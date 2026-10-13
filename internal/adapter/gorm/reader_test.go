package gorm_test

import (
	"context"
	"sync"
	"testing"
	"time"

	xologorm "github.com/xolo-gateway/xolo/internal/adapter/gorm"
	"github.com/xolo-gateway/xolo/internal/core/model"
	gormpkg "gorm.io/gorm"
)

// holdRead opens a long read on store and returns once it holds its
// connection. The read ends on release, or when the test does.
func holdRead(t *testing.T, store *xologorm.Store) (release func()) {
	t.Helper()
	started := make(chan struct{})
	released := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.ReadInventory(context.Background(), func(string, string) error {
			close(started)
			<-released
			return nil
		}, func(string, model.CommonItem) error { return nil })
	}()
	var once sync.Once
	release = func() { once.Do(func() { close(released) }) }
	t.Cleanup(func() {
		release()
		if err := <-done; err != nil {
			t.Errorf("long read: %v", err)
		}
	})
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("long read ended early: %v", err)
	}
	return release
}

// productionWriter opens the database as the server does: a single
// connection serves every write.
func productionWriter(t *testing.T, dsn string) *gormpkg.DB {
	t.Helper()
	db := openSQLite(t, dsn)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	return db
}

func TestSQLiteReaderLongReadDoesNotDelayLookups(t *testing.T) {
	dsn := newSQLiteDSN(t)
	store := newSeededStore(t, productionWriter(t, dsn), xologorm.WithReader(openSQLiteReader(t, dsn)))
	holdRead(t, store)

	ctx := context.Background()

	// The stores do not bind their queries to the context: a lookup queued
	// behind the long read would wait for it, so it runs aside.
	done := make(chan error, 1)
	go func() {
		_, err := store.GetTenantByID(ctx, testTenantID)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("lookup during a long read: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lookup waited for the long read")
	}

	// WAL mode: the open read does not block the writer either.
	if err := store.CreateOrg(ctx, model.NewOrganization(testTenantID, "acme", "Acme", "")); err != nil {
		t.Fatalf("write during a long read: %v", err)
	}
	if _, err := store.GetOrgBySlug(ctx, testTenantID, "acme"); err != nil {
		t.Fatalf("committed write unseen by the reader: %v", err)
	}
}

// Without a reader, the long read holds the only connection: the control
// case shows the test above measures the read pool.
func TestSQLiteWithoutReaderLongReadDelaysLookups(t *testing.T) {
	store := newSeededStore(t, productionWriter(t, newSQLiteDSN(t)))
	release := holdRead(t, store)

	done := make(chan error, 1)
	go func() {
		_, err := store.GetTenantByID(context.Background(), testTenantID)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("lookup did not wait for the long read: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("lookup after the long read: %v", err)
	}
}

func TestSQLiteReaderRefusesWrites(t *testing.T) {
	dsn := newSQLiteDSN(t)
	newSeededStore(t, openSQLite(t, dsn))
	reader := openSQLiteReader(t, dsn)

	if err := reader.Exec("DELETE FROM tenants").Error; err == nil {
		t.Fatal("expected the reader to refuse a write")
	}
}

func TestSQLiteReaderRefusesInMemoryDatabase(t *testing.T) {
	for _, dsn := range []string{":memory:", "file::memory:?cache=shared", "file:x?mode=memory"} {
		if _, err := xologorm.OpenSQLiteReader(dsn, time.Second, &gormpkg.Config{}); err == nil {
			t.Errorf("expected %q to be refused", dsn)
		}
	}
}
