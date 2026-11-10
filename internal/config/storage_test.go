package config

import (
	"fmt"
	"testing"
)

func TestStorageAutoMigrate(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)
	for _, value := range []string{"", "false", "true"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("XOLO_STORAGE_AUTO_MIGRATE", value)
			conf, err := Parse()
			if err != nil {
				t.Fatal(err)
			}
			if conf.Storage.AutoMigrate != (value != "false") {
				t.Fatalf("unexpected AutoMigrate for %q: %v", value, conf.Storage.AutoMigrate)
			}
		})
	}
}

func TestDatabasePoolIdleConns(t *testing.T) {
	t.Setenv("XOLO_SECRET_KEY", testSecretKey)
	t.Setenv("XOLO_STORAGE_DATABASE_POOL_MAX_OPEN_CONNS", "40")

	conf, err := Parse()
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if got := conf.Storage.Database.Pool.IdleConns(); got != 40 {
		t.Errorf("unset: got %d idle connections, want as many as open (40)", got)
	}

	for _, value := range []string{"10", "0"} {
		t.Setenv("XOLO_STORAGE_DATABASE_POOL_MAX_IDLE_CONNS", value)
		conf, err := Parse()
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		if got := conf.Storage.Database.Pool.IdleConns(); fmt.Sprint(got) != value {
			t.Errorf("set to %s: got %d", value, got)
		}
	}
}
