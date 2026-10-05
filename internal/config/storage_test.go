package config

import "testing"

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
